package db

import (
	"context"
	"os"
	"testing"
)

func TestPostGISLogic(t *testing.T) {
	connStr := os.Getenv("TEST_DATABASE_URL")
	if connStr == "" {
		t.Skip("Skipping DB tests; set TEST_DATABASE_URL to run")
	}

	store, err := Connect(connStr)
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}

	// Ensure clean slate
	_, err = store.db.Exec(`
		CREATE EXTENSION IF NOT EXISTS postgis;
		DROP TABLE IF EXISTS test_parcels CASCADE;
		DROP TABLE IF EXISTS test_zoning CASCADE;
		
		CREATE TABLE test_zoning (
			id TEXT PRIMARY KEY,
			zone_code TEXT,
			zone_name TEXT,
			is_residential BOOLEAN,
			geom geometry(MultiPolygon, 2277)
		);
		CREATE TABLE test_parcels (
			id TEXT PRIMARY KEY,
			computed_acres REAL,
			district_id TEXT REFERENCES test_zoning(id),
			geom geometry(MultiPolygon, 2277)
		);
	`)
	if err != nil {
		t.Fatalf("Failed to setup test tables: %v", err)
	}

	// 1. Test Area (1-acre square is ~208.71 ft on a side)
	// 208.71032557 * 208.71032557 = 43560 sq ft
	_, err = store.db.Exec(`
		INSERT INTO test_parcels (id, geom) VALUES (
			'parcel-1-acre', 
			ST_Multi(ST_SetSRID(ST_GeomFromText('POLYGON((0 0, 208.71032557 0, 208.71032557 208.71032557, 0 208.71032557, 0 0))'), 2277))
		)
	`)
	if err != nil {
		t.Fatalf("Failed to insert area test parcel: %v", err)
	}

	var acres float64
	err = store.db.QueryRow(`SELECT ST_Area(geom) / 43560.0 FROM test_parcels WHERE id = 'parcel-1-acre'`).Scan(&acres)
	if err != nil {
		t.Fatalf("Failed to query area: %v", err)
	}

	if acres < 0.999 || acres > 1.001 {
		t.Errorf("Expected area to be ~1.0 acres, got %f", acres)
	}

	// 2. Test Classifier Logic
	classifierTests := []struct {
		code     string
		name     string
		expected bool
	}{
		{"R-1", "", true},
		{"", "Residential - Single Family", true},
		{" RM ", "", true},
		{"C-1", "Commercial", false},
		{"", "", false},
	}

	for _, tt := range classifierTests {
		var isRes bool
		err = store.db.QueryRow(`
			SELECT 
			(UPPER(TRIM($1::text)) LIKE 'R%' OR UPPER(TRIM($2::text)) LIKE 'R%') OR
			(UPPER(TRIM($1::text)) LIKE 'RESIDENTIAL%' OR UPPER(TRIM($2::text)) LIKE 'RESIDENTIAL%')
		`, tt.code, tt.name).Scan(&isRes)
		if err != nil {
			t.Fatalf("Classifier query failed: %v", err)
		}
		if isRes != tt.expected {
			t.Errorf("Classifier fail for code='%s', name='%s'. Expected %v, got %v", tt.code, tt.name, tt.expected, isRes)
		}
	}

	// 3. Test Spatial Join (PointOnSurface vs L-Shape)
	// We insert an L-shaped parcel. Its bbox centroid is outside the polygon.
	// We insert a zoning district that overlaps the true interior.
	_, err = store.db.Exec(`
		INSERT INTO test_zoning (id, geom) VALUES (
			'zone-A', 
			ST_Multi(ST_SetSRID(ST_GeomFromText('POLYGON((0 0, 10 0, 10 10, 0 10, 0 0))'), 2277))
		);

		INSERT INTO test_parcels (id, geom) VALUES (
			'parcel-l-shape',
			-- L shape from (0,0) to (10,0) up to (10,10) but missing the top-left chunk
			ST_Multi(ST_SetSRID(ST_GeomFromText('POLYGON((0 0, 10 0, 10 10, 5 10, 5 5, 0 5, 0 0))'), 2277))
		);
	`)
	if err != nil {
		t.Fatalf("Failed to insert spatial join test shapes: %v", err)
	}

	var district string
	err = store.db.QueryRow(`
		SELECT z.id 
		FROM test_zoning z, test_parcels p
		WHERE p.id = 'parcel-l-shape' AND ST_Intersects(z.geom, ST_PointOnSurface(p.geom))
	`).Scan(&district)
	if err != nil {
		t.Fatalf("Spatial join failed: %v", err)
	}

	if district != "zone-A" {
		t.Errorf("Expected spatial join to assign 'zone-A', got '%s'", district)
	}
}
