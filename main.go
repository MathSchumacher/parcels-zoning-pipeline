package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"

	"takehome/db"
	"takehome/ingest"
)

const (
	// Official Hays County GIS parcels service (owner: HaysCountyGIS on
	// ArcGIS Online; the brief links the Hub page that fronts it).
	parcelsURL = "https://services5.arcgis.com/bVphnK8rPe5MHUSr/arcgis/rest/services/Hays_County_Parcels/FeatureServer/0"
	// City of Buda zoning service.
	zoningURL = "https://services6.arcgis.com/vXZW4vAaPRr14z2s/arcgis/rest/services/Zoning/FeatureServer/0"
	// NAD83 Texas South Central (US survey feet) — Hays County's official
	// state-plane zone and what its GIS serves natively. Buda publishes
	// zoning in Texas Central (2277); both feeds are reprojected to 2278
	// server-side so all area math happens in one CRS, in feet.
	outSR = "2278"
)

func main() {
	dbPath := flag.String("db", "spatialdb.sqlite", "path to the SQLite database file")
	transformOnly := flag.Bool("transform-only", false, "skip fetching; rebuild derived tables and stats from stored raw data")
	flag.Parse()

	ctx := context.Background()
	connStr := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)", *dbPath)

	fmt.Println("Connecting to SQLite database...")
	store, err := db.Connect(connStr)
	if err != nil {
		log.Fatalf("Failed to connect to db: %v", err)
	}

	fmt.Println("Initializing Schema...")
	if err := store.InitSchema("db/schema.sql"); err != nil {
		log.Fatalf("Failed to init schema: %v", err)
	}

	if !*transformOnly {
		fmt.Println("Fetching Buda Zoning...")
		if err := ingestLayer(ctx, zoningURL, store.UpsertRawZoning); err != nil {
			log.Fatalf("Failed to ingest zoning: %v", err)
		}

		fmt.Println("Fetching Hays Parcels (this may take a while)...")
		if err := ingestLayer(ctx, parcelsURL, store.UpsertRawParcels); err != nil {
			log.Fatalf("Failed to ingest parcels: %v", err)
		}
	}

	fmt.Println("Running Spatial Transformations in Go...")
	tstats, err := store.RunTransform(ctx)
	if err != nil {
		log.Fatalf("Failed to run transform: %v", err)
	}
	fmt.Printf("Transform: %d zones (%d skipped: bad geometry), %d parcels (%d skipped: bad geometry, %d outside any zoning district)\n",
		tstats.Zones, tstats.ZonesSkipped, tstats.Parcels, tstats.ParcelsSkipped, tstats.ParcelsUnzoned)

	fmt.Println("Querying final stats...")
	stats, err := store.GetStats(ctx)
	if err != nil {
		log.Fatalf("Failed to get stats: %v", err)
	}

	fmt.Println("\n--- RESIDENTIAL PARCELS > 1 ACRE ---")
	b, _ := json.MarshalIndent(stats, "", "  ")
	fmt.Println(string(b))
}

// ingestLayer streams pages from the service straight into the raw store, so
// a 120k-feature layer never has to fit in memory at once.
func ingestLayer(ctx context.Context, url string, upsert func(context.Context, []ingest.Feature, string) (int, error)) error {
	total, skipped := 0, 0
	err := ingest.FetchLayerPages(url, "OBJECTID", outSR, func(page []ingest.Feature) error {
		sk, err := upsert(ctx, page, "OBJECTID")
		if err != nil {
			return err
		}
		total += len(page)
		skipped += sk
		return nil
	})
	if err != nil {
		return err
	}
	fmt.Printf("Ingested %d features (%d skipped: missing geometry)\n", total, skipped)
	return nil
}
