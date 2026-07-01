package db

import (
	"context"
	"encoding/json"
	"testing"

	"takehome/ingest"
)

func TestSQLitePipeline(t *testing.T) {
	// Connect to in-memory SQLite database
	store, err := Connect("file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer store.db.Close()

	ctx := context.Background()

	// Initialize the schema for the test
	_, err = store.db.Exec(`
		CREATE TABLE raw_parcels (source_id TEXT PRIMARY KEY, attrs TEXT, geom TEXT);
		CREATE TABLE raw_zoning (source_id TEXT PRIMARY KEY, zone_code TEXT, zone_name TEXT, geom TEXT);
		CREATE TABLE zoning_districts (id TEXT PRIMARY KEY, zone_code TEXT, zone_name TEXT, is_residential BOOLEAN);
		CREATE TABLE parcels (id TEXT PRIMARY KEY, computed_acres REAL, district_id TEXT REFERENCES zoning_districts(id));
	`)
	if err != nil {
		t.Fatalf("Schema init failed: %v", err)
	}

	// 1. Mock Ingest Zoning
	zoningFeatures := []ingest.Feature{
		{
			Properties: map[string]interface{}{
				"OBJECTID":           1,
				"Zoning_Category":    "R-1",
				"Zoning_Description": "Single Family",
			},
			Geometry: json.RawMessage(`{
				"type": "Polygon",
				"coordinates": [[[0, 0], [1000, 0], [1000, 1000], [0, 1000], [0, 0]]]
			}`),
		},
	}
	err = store.UpsertRawZoning(ctx, zoningFeatures, "OBJECTID")
	if err != nil {
		t.Fatalf("Failed to upsert raw zoning: %v", err)
	}

	// 2. Mock Ingest Parcels
	parcelFeatures := []ingest.Feature{
		{
			// Parcel 1: Inside R-1, > 1 acre (1000x1000 = 1000000 sq ft = 22.9 acres)
			Properties: map[string]interface{}{"OBJECTID": 100},
			Geometry: json.RawMessage(`{
				"type": "Polygon",
				"coordinates": [[[10, 10], [990, 10], [990, 990], [10, 990], [10, 10]]]
			}`),
		},
		{
			// Parcel 2: Inside R-1, < 1 acre (100x100 = 10000 sq ft = 0.22 acres) -> Filtered out!
			Properties: map[string]interface{}{"OBJECTID": 101},
			Geometry: json.RawMessage(`{
				"type": "Polygon",
				"coordinates": [[[10, 10], [110, 10], [110, 110], [10, 110], [10, 10]]]
			}`),
		},
		{
			// Parcel 3: Outside R-1 -> Filtered out!
			Properties: map[string]interface{}{"OBJECTID": 102},
			Geometry: json.RawMessage(`{
				"type": "Polygon",
				"coordinates": [[[2000, 2000], [3000, 2000], [3000, 3000], [2000, 3000], [2000, 2000]]]
			}`),
		},
	}
	err = store.UpsertRawParcels(ctx, parcelFeatures, "OBJECTID")
	if err != nil {
		t.Fatalf("Failed to upsert raw parcels: %v", err)
	}

	// 3. Run Transform (loads raw data, computes geometry in Go, saves to derived tables)
	if err := store.RunTransform(ctx); err != nil {
		t.Fatalf("RunTransform failed: %v", err)
	}

	// 4. Get Stats
	stats, err := store.GetStats(ctx)
	if err != nil {
		t.Fatalf("GetStats failed: %v", err)
	}

	// Assertions
	if len(stats) != 1 {
		t.Fatalf("Expected exactly 1 grouped area in stats, got %d", len(stats))
	}

	group := stats[0]
	if group["area"] != "R-1" {
		t.Errorf("Expected area to be 'R-1', got '%v'", group["area"])
	}
	if group["parcel_count"] != 1 {
		t.Errorf("Expected exactly 1 parcel in R-1 to be counted, got %v", group["parcel_count"])
	}
	
	medianAcres := group["median_acres"].(float64)
	// 980x980 ft = 960,400 sq ft = 22.047 acres
	if medianAcres < 22.0 || medianAcres > 22.1 {
		t.Errorf("Expected median acres to be ~22.04, got %f", medianAcres)
	}
}
