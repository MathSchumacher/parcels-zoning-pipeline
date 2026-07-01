# Implementation Plan: Parcels & Zoning Data Product

*Revised based on senior architectural review.*

## Architecture & System Decomposition

We are building a **CLI Data Pipeline**, cutting the UI entirely to focus the 3-4 hour budget on core data engineering, idempotency, and clean spatial processing.

1.  **Ingestion (Go):** Fetches from Hays County and Buda ArcGIS REST APIs.
    *   *Sturdiness*: Loops with `resultOffset`/`resultRecordCount`, uses `orderByFields` on a stable ID to avoid pagination bugs, and implements retry-with-backoff.
    *   *CRS & Format*: Requests `outSR=2277` (Texas Central Feet) and `f=geojson` (to avoid Esri ring-winding issues).
2.  **Raw Store (PostgreSQL + PostGIS):** 
    *   Tables `raw_parcels` and `raw_zoning` storing raw geometry and source attributes.
    *   *Idempotency*: Uses `INSERT ... ON CONFLICT (source_id) DO UPDATE` to handle duplicates and allow clean re-runs.
3.  **Transformation & Core Logic (PostGIS):**
    *   **Area**: `ST_Area(geom) / 43560` (accurate and simple because CRS is in feet).
    *   **Spatial Join**: Uses `ST_Intersects` with `ST_PointOnSurface(parcel.geom)` to avoid crescent/L-shape centroid misses. Uses GiST indexes for O(log N) performance, avoiding O(N*M) waste.
    *   **Residential Rule**: Checks both code and name fields: `UPPER(TRIM(zone_code)) LIKE 'R%' OR UPPER(TRIM(zone_name)) LIKE 'R%'`.
4.  **Normalized View / Stats**: A derived table/view that runs the final grouping query. The CLI simply prints this table to stdout.

## Key Decisions & Deferrals

*   **Decision (PostGIS vs Pure Go)**: I am explicitly choosing not to reinvent computational geometry. Hand-rolling shoelace and ray-casting algorithms introduces subtle bugs (holes, multipart geometries). PostGIS handles CRS, `ST_Area`, and `ST_PointOnSurface` natively. *Trade-off*: Adds a Docker dependency, but buys massive defensibility and trivializes the "1km" stretch goal.
*   **Decision (PointOnSurface vs Full Overlap)**: We use `ST_PointOnSurface` to assign a parcel to a zoning district. *Deferred*: Majority-area overlap (`ST_Intersection` + area ratio) is a more accurate but complex solution for parcels that straddle zoning boundaries.
*   **Decision (CLI over UI)**: A CLI tool demonstrates scope discipline. We defer visual rendering to focus on data integrity.

## Requirements Elicitation (Questions for Stakeholders)

The final deliverable will prominently feature these questions:
1.  **What does "by area" mean?** Grouping residential parcels by "zoning category" is mildly circular. Does the business actually want stats grouped by a named subdivision, council district, or a spatial grid (e.g., H3)?
2.  **The "R" Prefix False Positives:** The rule `starts with R` will catch "REC" (Recreation) or "ROW" (Right of Way). Should we maintain an explicit exclusion list?
3.  **Unzoned Parcels:** Hays County parcels cover the whole county; Buda Zoning only covers the city. How should we bucket the tens of thousands of parcels that fall outside city limits?

## Execution Steps (TDD)
1.  **Setup**: `docker-compose.yml` for PostGIS. Init Go module.
2.  **Test Ingestion**: Test pagination logic and retry mechanics against a mock HTTP server.
3.  **Test Spatial Logic**: Execute SQL tests against PostGIS inserting a known 1-acre square and verifying `ST_Area`; insert overlapping/non-overlapping shapes to test `ST_PointOnSurface`.
4.  **Build Pipeline**: Write the CLI command to trigger ingestion, run the `.sql` transformations, and print the results.
