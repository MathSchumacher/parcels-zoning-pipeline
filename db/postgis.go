package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"takehome/ingest"

	_ "github.com/lib/pq"
)

type Store struct {
	db *sql.DB
}

func Connect(connStr string) (*Store, error) {
	db, err := sql.Open("postgres", connStr)
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
		VALUES ($1, $2, ST_Multi(ST_SetSRID(ST_GeomFromGeoJSON($3), 2277)))
		ON CONFLICT (source_id) DO UPDATE SET attrs = EXCLUDED.attrs, geom = EXCLUDED.geom
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
			continue // skip empty geometries
		}

		_, err := stmt.ExecContext(ctx, idStr, string(attrsJSON), geomJSON)
		if err != nil {
			return fmt.Errorf("failed to insert parcel %s: %w", idStr, err)
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
		VALUES ($1, $2, $3, ST_Multi(ST_SetSRID(ST_GeomFromGeoJSON($4), 2277)))
		ON CONFLICT (source_id) DO UPDATE SET zone_code = EXCLUDED.zone_code, zone_name = EXCLUDED.zone_name, geom = EXCLUDED.geom
	`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, f := range features {
		idVal := f.Properties[idField]
		idStr := fmt.Sprintf("%v", idVal)

		// The specific fields might vary, using placeholders that the transform step can rely on
		// Hays County uses different fields for zoning code/name. For Buda, "Zoning_Category" is the code.
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
			return fmt.Errorf("failed to insert zoning %s: %w", idStr, err)
		}
	}

	return tx.Commit()
}

func (s *Store) RunTransform(ctx context.Context) error {
	// Rebuild derived tables
	query := `
		TRUNCATE TABLE zoning_districts CASCADE;

		INSERT INTO zoning_districts (id, zone_code, zone_name, is_residential, geom)
		SELECT 
			source_id, 
			zone_code, 
			zone_name, 
			(UPPER(TRIM(zone_code)) LIKE 'R%' OR UPPER(TRIM(zone_name)) LIKE 'R%') OR
			(UPPER(TRIM(zone_code)) LIKE 'RESIDENTIAL%' OR UPPER(TRIM(zone_name)) LIKE 'RESIDENTIAL%'),
			geom
		FROM raw_zoning;

		TRUNCATE TABLE parcels CASCADE;

		INSERT INTO parcels (id, computed_acres, district_id, geom)
		SELECT 
			p.source_id,
			ST_Area(p.geom) / 43560.0,
			(
				SELECT z.id 
				FROM zoning_districts z 
				WHERE ST_Intersects(z.geom, ST_PointOnSurface(p.geom))
				LIMIT 1
			),
			p.geom
		FROM raw_parcels p;
	`
	_, err := s.db.ExecContext(ctx, query)
	return err
}

func (s *Store) GetStats(ctx context.Context) ([]map[string]interface{}, error) {
	query := `
		SELECT
			z.zone_code                             AS area,
			COUNT(*)                                AS parcel_count,
			percentile_cont(0.5) WITHIN GROUP (ORDER BY p.computed_acres) AS median_acres
		FROM parcels p
		JOIN zoning_districts z ON z.id = p.district_id
		WHERE z.is_residential = true
		  AND p.computed_acres > 1.0
		GROUP BY z.zone_code
		ORDER BY parcel_count DESC;
	`
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	cols, _ := rows.Columns()
	var results []map[string]interface{}

	for rows.Next() {
		columns := make([]interface{}, len(cols))
		columnPointers := make([]interface{}, len(cols))
		for i := range columns {
			columnPointers[i] = &columns[i]
		}

		if err := rows.Scan(columnPointers...); err != nil {
			return nil, err
		}

		m := make(map[string]interface{})
		for i, colName := range cols {
			val := columnPointers[i].(*interface{})
			m[colName] = *val
		}
		results = append(results, m)
	}
	return results, nil
}
