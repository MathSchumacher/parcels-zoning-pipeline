package db

import (
	"context"
	"encoding/json"
	"testing"

	"takehome/ingest"
)

// newTestStore opens a uniquely named in-memory database (so tests don't
// share state) and applies the real schema file, keeping tests and schema
// from drifting apart.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Connect("file:" + t.Name() + "?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	t.Cleanup(func() { store.db.Close() })

	if err := store.InitSchema("schema.sql"); err != nil {
		t.Fatalf("Schema init failed: %v", err)
	}
	return store
}

func zoningFixtures() []ingest.Feature {
	return []ingest.Feature{
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
}

func parcelFixtures() []ingest.Feature {
	return []ingest.Feature{
		{
			// Parcel 1: Inside R-1, > 1 acre (980x980 = 960,400 sq ft = 22.05 acres)
			Properties: map[string]interface{}{"OBJECTID": 100, "hood_cd": "BUDA_N1"},
			Geometry: json.RawMessage(`{
				"type": "Polygon",
				"coordinates": [[[10, 10], [990, 10], [990, 990], [10, 990], [10, 10]]]
			}`),
		},
		{
			// Parcel 2: Inside R-1, < 1 acre (100x100 = 10000 sq ft = 0.22 acres) -> Filtered out!
			Properties: map[string]interface{}{"OBJECTID": 101, "hood_cd": "BUDA_N1"},
			Geometry: json.RawMessage(`{
				"type": "Polygon",
				"coordinates": [[[10, 10], [110, 10], [110, 110], [10, 110], [10, 10]]]
			}`),
		},
		{
			// Parcel 3: Outside R-1 -> unzoned, excluded from stats
			Properties: map[string]interface{}{"OBJECTID": 102},
			Geometry: json.RawMessage(`{
				"type": "Polygon",
				"coordinates": [[[2000, 2000], [3000, 2000], [3000, 3000], [2000, 3000], [2000, 2000]]]
			}`),
		},
		{
			// Parcel 4: No geometry -> skipped at ingest, visibly
			Properties: map[string]interface{}{"OBJECTID": 103},
			Geometry:   json.RawMessage(`null`),
		},
	}
}

func TestSQLitePipeline(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// 1. Ingest
	skippedZ, err := store.UpsertRawZoning(ctx, zoningFixtures(), "OBJECTID")
	if err != nil {
		t.Fatalf("Failed to upsert raw zoning: %v", err)
	}
	if skippedZ != 0 {
		t.Errorf("Expected 0 zoning features skipped, got %d", skippedZ)
	}

	skippedP, err := store.UpsertRawParcels(ctx, parcelFixtures(), "OBJECTID")
	if err != nil {
		t.Fatalf("Failed to upsert raw parcels: %v", err)
	}
	if skippedP != 1 {
		t.Errorf("Expected 1 parcel skipped (null geometry), got %d", skippedP)
	}

	// 2. Transform
	tstats, err := store.RunTransform(ctx)
	if err != nil {
		t.Fatalf("RunTransform failed: %v", err)
	}
	if tstats.Zones != 1 || tstats.ZonesSkipped != 0 {
		t.Errorf("Expected 1 zone / 0 skipped, got %d / %d", tstats.Zones, tstats.ZonesSkipped)
	}
	if tstats.Parcels != 3 || tstats.ParcelsSkipped != 0 {
		t.Errorf("Expected 3 parcels / 0 skipped, got %d / %d", tstats.Parcels, tstats.ParcelsSkipped)
	}
	if tstats.ParcelsUnzoned != 1 {
		t.Errorf("Expected 1 unzoned parcel (outside the district), got %d", tstats.ParcelsUnzoned)
	}

	// 3. Stats
	stats, err := store.GetStats(ctx)
	if err != nil {
		t.Fatalf("GetStats failed: %v", err)
	}

	if len(stats.ByZoningDistrict) != 1 {
		t.Fatalf("Expected exactly 1 zoning-district group, got %d", len(stats.ByZoningDistrict))
	}
	zg := stats.ByZoningDistrict[0]
	if zg.Area != "R-1" {
		t.Errorf("Expected area to be 'R-1', got '%v'", zg.Area)
	}
	if zg.ParcelCount != 1 {
		t.Errorf("Expected exactly 1 parcel in R-1 to be counted, got %v", zg.ParcelCount)
	}
	// 980x980 ft = 960,400 sq ft = 22.047 acres
	if zg.MedianAcres < 22.0 || zg.MedianAcres > 22.1 {
		t.Errorf("Expected median acres to be ~22.04, got %f", zg.MedianAcres)
	}

	if len(stats.ByNeighborhood) != 1 {
		t.Fatalf("Expected exactly 1 neighborhood group, got %d", len(stats.ByNeighborhood))
	}
	ng := stats.ByNeighborhood[0]
	if ng.Area != "BUDA_N1" {
		t.Errorf("Expected neighborhood 'BUDA_N1', got '%v'", ng.Area)
	}
	if ng.ParcelCount != 1 {
		t.Errorf("Expected 1 parcel in BUDA_N1, got %v", ng.ParcelCount)
	}
}

// The brief's sturdiness requirement: running the pipeline twice — or against
// a corrected version of the data — must not leave a mess.
func TestPipelineIdempotent(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		if _, err := store.UpsertRawZoning(ctx, zoningFixtures(), "OBJECTID"); err != nil {
			t.Fatalf("Upsert zoning run %d failed: %v", i+1, err)
		}
		if _, err := store.UpsertRawParcels(ctx, parcelFixtures(), "OBJECTID"); err != nil {
			t.Fatalf("Upsert parcels run %d failed: %v", i+1, err)
		}
		if _, err := store.RunTransform(ctx); err != nil {
			t.Fatalf("Transform run %d failed: %v", i+1, err)
		}
	}

	counts := map[string]int{}
	for _, table := range []string{"raw_zoning", "raw_parcels", "zoning_districts", "parcels"} {
		var n int
		if err := store.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
			t.Fatalf("Count %s failed: %v", table, err)
		}
		counts[table] = n
	}

	if counts["raw_zoning"] != 1 || counts["raw_parcels"] != 3 {
		t.Errorf("Expected raw counts 1/3 after double ingest, got %d/%d", counts["raw_zoning"], counts["raw_parcels"])
	}
	if counts["zoning_districts"] != 1 || counts["parcels"] != 3 {
		t.Errorf("Expected derived counts 1/3 after double transform, got %d/%d", counts["zoning_districts"], counts["parcels"])
	}

	// A corrected upstream value must overwrite, not duplicate.
	corrected := zoningFixtures()
	corrected[0].Properties["Zoning_Description"] = "Single Family (corrected)"
	if _, err := store.UpsertRawZoning(ctx, corrected, "OBJECTID"); err != nil {
		t.Fatalf("Corrected upsert failed: %v", err)
	}
	var name string
	if err := store.db.QueryRow("SELECT zone_name FROM raw_zoning WHERE source_id = '1'").Scan(&name); err != nil {
		t.Fatalf("Read corrected zoning failed: %v", err)
	}
	if name != "Single Family (corrected)" {
		t.Errorf("Expected corrected name to overwrite, got %q", name)
	}
}
