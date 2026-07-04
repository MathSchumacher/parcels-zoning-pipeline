# Parcels & Zoning — Take-Home

Ingests Hays County parcels and City of Buda zoning from their ArcGIS REST services, normalizes both into SQLite, and answers the core question: **residential parcels bigger than one acre, with lot size computed from the geometry, rolled up into count and median lot size by area.**

Pure Go + pure-Go SQLite — no Docker, no C compiler, no external services to install.

## Deliverables map

| Brief deliverable | Where |
|---|---|
| Design document (decomposition, diagram, decisions & deferrals) | [take_home_design.md](take_home_design.md) §1–4 |
| Requirements: assumptions & stakeholder questions | [take_home_design.md](take_home_design.md) §5 |
| Working skeleton (schema, ingestion, core query) | this repo — `db/`, `ingest/`, `spatial/`, `main.go` |
| Process & AI note | below |

## How to run

Prerequisites: Go 1.26+. Then:

```sh
go run .                    # full pipeline: fetch both feeds (~121k parcels, a few minutes), transform, print stats
go run . -transform-only    # skip fetching; rebuild derived tables + stats from stored raw data
go run . -db other.sqlite   # use a different database file
go test ./...               # unit tests (no network needed)
```

Running it twice is safe by design: raw ingestion is upsert-only (keyed by source ID), and the derived tables are rebuilt from raw inside a single transaction on every run.

## Sample output (real run)

From a full run on 2026-07-04 (121k parcels, a few minutes of fetching):

```
Ingested 126 features (0 skipped: missing geometry)      # Buda zoning
Ingested 120913 features (0 skipped: missing geometry)   # Hays parcels
Transform: 126 zones (0 skipped: bad geometry), 120913 parcels
           (0 skipped: bad geometry, 114395 outside any zoning district)

--- RESIDENTIAL PARCELS > 1 ACRE ---
{
  "by_neighborhood": [
    { "area": "OXBW",       "parcel_count": 74, "median_acres": 2.00 },
    { "area": "NONE",       "parcel_count": 55, "median_acres": 3.23 },
    { "area": "2ABS",       "parcel_count": 21, "median_acres": 18.92 },
    { "area": "PERS",       "parcel_count": 9,  "median_acres": 3.44 },
    { "area": "C-BUDA-NWX", "parcel_count": 6,  "median_acres": 18.99 },
    { "area": "(none)",     "parcel_count": 5,  "median_acres": 2.00 },
    { "area": "COLONY",     "parcel_count": 5,  "median_acres": 1.97 },
    ... 7 smaller groups ...
  ],
  "by_zoning_district": [
    { "area": "R1",    "parcel_count": 76, "median_acres": 2.00 },
    { "area": "R2",    "parcel_count": 59, "median_acres": 3.81 },
    { "area": "R3",    "parcel_count": 27, "median_acres": 2.48 },
    { "area": "R3/R4", "parcel_count": 19, "median_acres": 13.50 },
    { "area": "R2-C",  "parcel_count": 3,  "median_acres": 2.31 },
    { "area": "R5",    "parcel_count": 1,  "median_acres": 16.40 }
  ]
}
```

185 residential parcels over one acre in total; both axes sum to the same 185, and R1 ("Estate Residential") showing a ~2-acre median is a sanity check that passes on its face.

Notes on the numbers: only parcels that fall inside a *residential* Buda zoning district and exceed one computed acre are rolled up; the vast majority of the county's parcels sit outside Buda's zoning entirely (reported as `outside any zoning district`, not silently dropped). `(none)` groups parcels whose appraisal record has no neighborhood code — and the data also contains a literal `"NONE"` code, which is kept distinct on purpose: collapsing them is a data-cleaning decision I'd rather surface than bury.

## Process & AI note

**How I worked.** Reconnaissance first — hitting both services' metadata endpoints to inspect fields, spatial references, and counts before writing code — then schema, ingestion, transform, stats, with the design doc updated alongside. The original plan was PostGIS in Docker; I pivoted to pure Go + SQLite when the Docker dependency proved to be more friction than value for a skeleton this size (the git history records the pivot).

**How I used AI.** Claude (Anthropic) drafted most of the code, tests, and documentation, iterating under review: I set the constraints (pure Go, a hard raw/derived seam, idempotent upserts, compute area from geometry only) and reviewed the diffs.

**What checks I applied to its output.** This matters because the checks caught real bugs:

- **Live-service verification.** Every URL, field name, and CRS assumption was checked against the real services with `curl` before being trusted. This caught an AI-hallucinated parcels URL (a plausible-looking FeatureServer path on the wrong ArcGIS org that returns `Invalid URL`) — replaced with the county's actual service after searching ArcGIS Online for the authoritative owner.
- **A full end-to-end run, reading the numbers.** The first complete run ingested exactly 2,000 parcels — suspiciously equal to one page. Root cause: with `f=geojson`, ArcGIS nests the `exceededTransferLimit` pagination flag inside a `properties` object instead of at the top level, so pagination stopped after page one. The unit tests hadn't caught it because the mock server mirrored the assumed shape, not the real one; the mock was fixed to match reality along with the parser.
- **Unit tests targeting the failure modes, not just the happy path:** a known 1-acre square for the area math; a U-shaped parcel whose centroid falls *outside* the polygon (proving the point-on-surface fallback works); the residential classifier tested against real Buda district values, including `PD — "Reserve at Cole Springs"` (a starts-with-R name that must *not* classify as residential); a CRS-mismatch response that must abort ingestion; and a double-ingest/double-transform idempotency test.
