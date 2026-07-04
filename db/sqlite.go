package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"

	_ "modernc.org/sqlite"
	"takehome/ingest"
	"takehome/spatial"

	"github.com/paulmach/orb"
)

type Store struct {
	db *sql.DB
}

func Connect(connStr string) (*Store, error) {
	db, err := sql.Open("sqlite", connStr)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) InitSchema(schemaFile string) error {
	bytes, err := os.ReadFile(schemaFile)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(string(bytes))
	return err
}

// UpsertRawParcels upserts features keyed by source ID and reports how many
// were skipped for missing geometry, so data loss is visible, not silent.
func (s *Store) UpsertRawParcels(ctx context.Context, features []ingest.Feature, idField string) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO raw_parcels (source_id, attrs, geom)
		VALUES (?, ?, ?)
		ON CONFLICT (source_id) DO UPDATE SET attrs = excluded.attrs, geom = excluded.geom
	`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()

	skipped := 0
	for _, f := range features {
		geomJSON := string(f.Geometry)
		if geomJSON == "null" || geomJSON == "" {
			skipped++
			continue
		}

		idStr := fmt.Sprintf("%v", f.Properties[idField])
		attrsJSON, err := json.Marshal(f.Properties)
		if err != nil {
			return skipped, err
		}

		if _, err := stmt.ExecContext(ctx, idStr, string(attrsJSON), geomJSON); err != nil {
			return skipped, err
		}
	}
	return skipped, tx.Commit()
}

// UpsertRawZoning upserts zoning features keyed by source ID and reports how
// many were skipped for missing geometry.
func (s *Store) UpsertRawZoning(ctx context.Context, features []ingest.Feature, idField string) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO raw_zoning (source_id, zone_code, zone_name, geom)
		VALUES (?, ?, ?, ?)
		ON CONFLICT (source_id) DO UPDATE SET zone_code = excluded.zone_code, zone_name = excluded.zone_name, geom = excluded.geom
	`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()

	skipped := 0
	for _, f := range features {
		geomJSON := string(f.Geometry)
		if geomJSON == "null" || geomJSON == "" {
			skipped++
			continue
		}

		idStr := fmt.Sprintf("%v", f.Properties[idField])

		codeVal := ""
		if val, ok := f.Properties["Zoning_Category"]; ok && val != nil {
			codeVal = fmt.Sprintf("%v", val)
		}

		nameVal := ""
		if val, ok := f.Properties["Zoning_Description"]; ok && val != nil {
			nameVal = fmt.Sprintf("%v", val)
		}

		if _, err := stmt.ExecContext(ctx, idStr, codeVal, nameVal, geomJSON); err != nil {
			return skipped, err
		}
	}
	return skipped, tx.Commit()
}

// TransformStats reports what the transform kept and what it dropped.
type TransformStats struct {
	Zones          int `json:"zones"`
	ZonesSkipped   int `json:"zones_skipped_bad_geom"`
	Parcels        int `json:"parcels"`
	ParcelsSkipped int `json:"parcels_skipped_bad_geom"`
	ParcelsUnzoned int `json:"parcels_unzoned"`
}

// RunTransform rebuilds the derived tables from raw using pure Go spatial
// math. The whole rebuild (deletes included) runs in one transaction, so a
// failed transform leaves the previous derived data intact.
func (s *Store) RunTransform(ctx context.Context) (TransformStats, error) {
	var stats TransformStats

	// Load zoning rows fully (and close the cursor) before the write tx.
	type zoneRow struct {
		id, code, name, geomStr string
	}
	var zoneRows []zoneRow

	rowsZ, err := s.db.QueryContext(ctx, `SELECT source_id, zone_code, zone_name, geom FROM raw_zoning`)
	if err != nil {
		return stats, err
	}
	for rowsZ.Next() {
		var z zoneRow
		if err := rowsZ.Scan(&z.id, &z.code, &z.name, &z.geomStr); err != nil {
			rowsZ.Close()
			return stats, err
		}
		zoneRows = append(zoneRows, z)
	}
	if err := rowsZ.Err(); err != nil {
		rowsZ.Close()
		return stats, err
	}
	rowsZ.Close()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return stats, err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `DELETE FROM parcels`); err != nil {
		return stats, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM zoning_districts`); err != nil {
		return stats, err
	}

	type loadedZone struct {
		id    string
		bound orb.Bound
		geom  orb.Geometry
	}
	var zones []loadedZone

	stmtZ, err := tx.PrepareContext(ctx, `INSERT INTO zoning_districts (id, zone_code, zone_name, is_residential) VALUES (?, ?, ?, ?)`)
	if err != nil {
		return stats, err
	}
	defer stmtZ.Close()

	for _, z := range zoneRows {
		g, err := spatial.ParseGeometry([]byte(z.geomStr))
		if err != nil {
			stats.ZonesSkipped++
			continue
		}
		if _, err := stmtZ.ExecContext(ctx, z.id, z.code, z.name, spatial.IsResidential(z.code, z.name)); err != nil {
			return stats, err
		}
		zones = append(zones, loadedZone{id: z.id, bound: g.Bound(), geom: g})
		stats.Zones++
	}

	stmtP, err := tx.PrepareContext(ctx, `INSERT INTO parcels (id, computed_acres, district_id, neighborhood) VALUES (?, ?, ?, ?)`)
	if err != nil {
		return stats, err
	}
	defer stmtP.Close()

	rowsP, err := s.db.QueryContext(ctx, `SELECT source_id, attrs, geom FROM raw_parcels`)
	if err != nil {
		return stats, err
	}
	defer rowsP.Close()

	for rowsP.Next() {
		var pID, pAttrs, pGeomStr string
		if err := rowsP.Scan(&pID, &pAttrs, &pGeomStr); err != nil {
			return stats, err
		}

		pGeom, err := spatial.ParseGeometry([]byte(pGeomStr))
		if err != nil {
			stats.ParcelsSkipped++
			continue
		}

		acres := spatial.CalculateAcres(pGeom)
		point := spatial.PointOnSurface(pGeom)

		var districtID *string
		for i := range zones {
			// Bounding-box precheck: ~120k parcels x ~126 districts makes
			// unconditional point-in-polygon tests the bottleneck.
			if !zones[i].bound.Contains(point) {
				continue
			}
			if spatial.Contains(zones[i].geom, point) {
				districtID = &zones[i].id
				break
			}
		}
		if districtID == nil {
			stats.ParcelsUnzoned++
		}

		var attrs map[string]interface{}
		_ = json.Unmarshal([]byte(pAttrs), &attrs)
		neighborhood := attrString(attrs, "hood_cd")

		if _, err := stmtP.ExecContext(ctx, pID, acres, districtID, neighborhood); err != nil {
			return stats, err
		}
		stats.Parcels++
	}
	if err := rowsP.Err(); err != nil {
		return stats, err
	}
	rowsP.Close()

	return stats, tx.Commit()
}

// attrString reads a source attribute as text; ArcGIS numeric fields arrive
// as float64 through JSON, so whole numbers are rendered without ".0".
func attrString(attrs map[string]interface{}, key string) string {
	v, ok := attrs[key]
	if !ok || v == nil {
		return ""
	}
	if f, ok := v.(float64); ok && f == math.Trunc(f) {
		return strconv.FormatInt(int64(f), 10)
	}
	return fmt.Sprintf("%v", v)
}

type AreaStat struct {
	Area        string  `json:"area"`
	ParcelCount int     `json:"parcel_count"`
	MedianAcres float64 `json:"median_acres"`
}

// Stats answers the core question — residential parcels over one acre —
// rolled up on two axes: the county appraisal neighborhood (hood_cd), which
// matches the brief's "per neighborhood" example, and the zoning district,
// kept for comparison.
type Stats struct {
	ByNeighborhood   []AreaStat `json:"by_neighborhood"`
	ByZoningDistrict []AreaStat `json:"by_zoning_district"`
}

func (s *Store) GetStats(ctx context.Context) (Stats, error) {
	var out Stats
	var err error
	if out.ByNeighborhood, err = s.statsBy(ctx, `p.neighborhood`); err != nil {
		return out, err
	}
	if out.ByZoningDistrict, err = s.statsBy(ctx, `z.zone_code`); err != nil {
		return out, err
	}
	return out, nil
}

func (s *Store) statsBy(ctx context.Context, keyExpr string) ([]AreaStat, error) {
	// SQLite lacks percentile_cont; medians are computed in Go.
	query := fmt.Sprintf(`
		SELECT COALESCE(NULLIF(%s, ''), '(none)'), p.computed_acres
		FROM parcels p
		JOIN zoning_districts z ON z.id = p.district_id
		WHERE z.is_residential = true
		  AND p.computed_acres > 1.0
	`, keyExpr)

	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	groups := make(map[string][]float64)
	for rows.Next() {
		var key string
		var acres float64
		if err := rows.Scan(&key, &acres); err != nil {
			return nil, err
		}
		groups[key] = append(groups[key], acres)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	results := make([]AreaStat, 0, len(groups))
	for key, acresList := range groups {
		results = append(results, AreaStat{
			Area:        key,
			ParcelCount: len(acresList),
			MedianAcres: median(acresList),
		})
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].ParcelCount != results[j].ParcelCount {
			return results[i].ParcelCount > results[j].ParcelCount
		}
		return results[i].Area < results[j].Area
	})
	return results, nil
}

func median(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	sort.Float64s(vals)
	mid := len(vals) / 2
	if len(vals)%2 == 0 {
		return (vals[mid-1] + vals[mid]) / 2.0
	}
	return vals[mid]
}
