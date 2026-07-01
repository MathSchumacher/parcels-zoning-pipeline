package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"takehome/db"
	"takehome/ingest"
)

const (
	parcelsURL = "https://services6.arcgis.com/vXZW4vAaPRr14z2s/arcgis/rest/services/Hays_County_Parcels/FeatureServer/0"
	zoningURL  = "https://services6.arcgis.com/vXZW4vAaPRr14z2s/arcgis/rest/services/Zoning/FeatureServer/0"
	connStr    = "file:spatialdb.sqlite?cache=shared"
)

func main() {
	ctx := context.Background()

	fmt.Println("Connecting to SQLite database...")
	store, err := db.Connect(connStr)
	if err != nil {
		log.Fatalf("Failed to connect to db: %v", err)
	}

	fmt.Println("Initializing Schema...")
	if err := store.InitSchema("db/schema.sql"); err != nil {
		log.Fatalf("Failed to init schema: %v", err)
	}

	fmt.Println("Fetching Buda Zoning...")
	zoning, err := ingest.FetchLayer(zoningURL, "OBJECTID", "2277")
	if err != nil {
		log.Fatalf("Failed to fetch zoning: %v", err)
	}
	fmt.Printf("Fetched %d zoning features. Upserting...\n", len(zoning))
	if err := store.UpsertRawZoning(ctx, zoning, "OBJECTID"); err != nil {
		log.Fatalf("Failed to upsert zoning: %v", err)
	}

	fmt.Println("Fetching Hays Parcels (this may take a while)...")
	parcels, err := ingest.FetchLayer(parcelsURL, "OBJECTID", "2277")
	if err != nil {
		log.Fatalf("Failed to fetch parcels: %v", err)
	}
	fmt.Printf("Fetched %d parcels. Upserting...\n", len(parcels))
	if err := store.UpsertRawParcels(ctx, parcels, "OBJECTID"); err != nil {
		log.Fatalf("Failed to upsert parcels: %v", err)
	}

	fmt.Println("Running Spatial Transformations in Go...")
	if err := store.RunTransform(ctx); err != nil {
		log.Fatalf("Failed to run transform: %v", err)
	}

	fmt.Println("Querying final stats...")
	stats, err := store.GetStats(ctx)
	if err != nil {
		log.Fatalf("Failed to get stats: %v", err)
	}

	fmt.Println("\n--- FINAL RESULTS ---")
	b, _ := json.MarshalIndent(stats, "", "  ")
	fmt.Println(string(b))
}
