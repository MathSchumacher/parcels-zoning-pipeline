package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"sort"

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

func (s *Store) UpsertRawParcels(ctx context.Context, features []ingest.Feature, idField string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO raw_parcels (source_id, attrs, geom)
		VALUES (?, ?, ?)
		ON CONFLICT (source_id) DO UPDATE SET attrs = excluded.attrs, geom = excluded.geom
	`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, f := range features {
		idVal := f.Properties[idField]
		idStr := fmt.Sprintf("%v", idVal)

		attrsJSON, _ := json.Marshal(f.Properties)
		geomJSON := string(f.Geometry)

		if geomJSON == "null" || geomJSON == "" {
			continue
		}

		_, err := stmt.ExecContext(ctx, idStr, string(attrsJSON), geomJSON)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) UpsertRawZoning(ctx context.Context, features []ingest.Feature, idField string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO raw_zoning (source_id, zone_code, zone_name, geom)
		VALUES (?, ?, ?, ?)
		ON CONFLICT (source_id) DO UPDATE SET zone_code = excluded.zone_code, zone_name = excluded.zone_name, geom = excluded.geom
	`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, f := range features {
		idVal := f.Properties[idField]
		idStr := fmt.Sprintf("%v", idVal)

		codeVal := ""
		if val, ok := f.Properties["Zoning_Category"]; ok && val != nil {
			codeVal = fmt.Sprintf("%v", val)
		}
		
		nameVal := ""
		if val, ok := f.Properties["Zoning_Description"]; ok && val != nil {
			nameVal = fmt.Sprintf("%v", val)
		}

		geomJSON := string(f.Geometry)
		if geomJSON == "null" || geomJSON == "" {
			continue
		}

		_, err := stmt.ExecContext(ctx, idStr, codeVal, nameVal, geomJSON)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

// RunTransform uses pure Go spatial algorithms instead of PostGIS
func (s *Store) RunTransform(ctx context.Context) error {
	// 1. Clear derived tables
	_, err := s.db.Exec(`DELETE FROM parcels; DELETE FROM zoning_districts;`)
	if err != nil {
		return err
	}

	// 2. Load Zoning Geometries into memory for Point-in-Polygon check
	rowsZ, err := s.db.Query(`SELECT source_id, zone_code, zone_name, geom FROM raw_zoning`)
	if err != nil {
		return err
	}
	defer rowsZ.Close()

	type ZoningDistrict struct {
		ID       string
		Code     string
		Name     string
		IsRes    bool
		Geometry string
	}

	type loadedZone struct {
		district ZoningDistrict
		geom     interface{}
	}
	var zones []loadedZone

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	
	stmtZ, err := tx.Prepare(`INSERT INTO zoning_districts (id, zone_code, zone_name, is_residential) VALUES (?, ?, ?, ?)`)
	if err != nil {
		return err
	}

	for rowsZ.Next() {
		var z ZoningDistrict
		if err := rowsZ.Scan(&z.ID, &z.Code, &z.Name, &z.Geometry); err != nil {
			return err
		}
		z.IsRes = spatial.IsResidential(z.Code, z.Name)
		
		g, err := spatial.ParseGeometry([]byte(z.Geometry))
		if err != nil {
			continue // skip invalid geometry
		}
		
		zones = append(zones, loadedZone{district: z, geom: g})
		stmtZ.Exec(z.ID, z.Code, z.Name, z.IsRes)
	}
	stmtZ.Close()

	// 3. Stream parcels and calculate metrics in Go
	stmtP, err := tx.Prepare(`INSERT INTO parcels (id, computed_acres, district_id) VALUES (?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmtP.Close()

	rowsP, err := s.db.Query(`SELECT source_id, geom FROM raw_parcels`)
	if err != nil {
		return err
	}
	defer rowsP.Close()

	for rowsP.Next() {
		var pID, pGeomStr string
		if err := rowsP.Scan(&pID, &pGeomStr); err != nil {
			return err
		}

		pGeom, err := spatial.ParseGeometry([]byte(pGeomStr))
		if err != nil {
			continue
		}

		acres := spatial.CalculateAcres(pGeom)
		centroid := spatial.GetCentroid(pGeom)

		var districtID *string
		for _, z := range zones {
			if spatial.Contains(z.geom.(orb.Geometry), centroid) {
				districtID = &z.district.ID
				break
			}
		}

		stmtP.Exec(pID, acres, districtID)
	}

	return tx.Commit()
}

func (s *Store) GetStats(ctx context.Context) ([]map[string]interface{}, error) {
	// SQLite lacks percentile_cont. Calculate median in Go.
	query := `
		SELECT
			z.zone_code, p.computed_acres
		FROM parcels p
		JOIN zoning_districts z ON z.id = p.district_id
		WHERE z.is_residential = true
		  AND p.computed_acres > 1.0
	`
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	groups := make(map[string][]float64)
	for rows.Next() {
		var zCode string
		var acres float64
		if err := rows.Scan(&zCode, &acres); err != nil {
			return nil, err
		}
		groups[zCode] = append(groups[zCode], acres)
	}

	var results []map[string]interface{}
	for code, acresList := range groups {
		sort.Float64s(acresList)
		count := len(acresList)
		var median float64
		if count > 0 {
			mid := count / 2
			if count%2 == 0 {
				median = (acresList[mid-1] + acresList[mid]) / 2.0
			} else {
				median = acresList[mid]
			}
		}
		
		results = append(results, map[string]interface{}{
			"area": code,
			"parcel_count": count,
			"median_acres": median,
		})
	}

	// Sort results by parcel_count desc
	sort.Slice(results, func(i, j int) bool {
		c1 := results[i]["parcel_count"].(int)
		c2 := results[j]["parcel_count"].(int)
		return c1 > c2
	})

	return results, nil
}
