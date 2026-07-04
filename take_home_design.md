# Parcels & Zoning Data Product — Design Document

A pipeline that ingests Hays County parcels and City of Buda zoning, normalizes both, and answers: **residential parcels larger than one acre, with lot size computed from geometry, rolled up into count and median lot size by area.**

Time-boxed to 3–4 hours. This document states what I built, the calls I made, the calls I deliberately deferred, and the questions I'd take back to stakeholders.

---

## 1. Data reconnaissance (what's wrong with the inputs)

A few minutes sizing up the feeds surfaced the issues that actually shaped the design:

- **The parcels link in the brief is not a service.** It points at an ArcGIS Hub page; the actual REST endpoint behind it is `Hays_County_Parcels` on the county's ArcGIS Online org (`services5.arcgis.com/bVphnK8rPe5MHUSr/...`, 120,913 parcels, 2,000-record page limit).
- **Two jurisdictions, mismatched coverage.** Zoning covers the *city* of Buda (126 districts); parcels cover the *whole county*. The overwhelming majority of parcels have no zoning at all — so the spatial join must be built to *not match* as the common case, not the exception.
- **The two feeds aren't even in the same state-plane zone.** Buda publishes zoning in **NAD83 Texas Central (EPSG:2277)**; Hays County serves parcels in **NAD83 Texas South Central (EPSG:2278)** — the county's official zone. Both are US-survey-feet CRSs. I have the services reproject both feeds to **2278** at ingest, so all area math happens in one CRS, in feet. The ingester **verifies the returned CRS on every page and fails loudly** if a service ignored `outSR` — silently computing "acres" on WGS84 degrees is the failure mode to fear.
- **Serving format.** ArcGIS FeatureServers return Esri JSON (`rings`) by default, where holes and multipart polygons are implied by ring winding rather than structured. Requesting `f=geojson` sidesteps a class of ring-orientation bugs — but moves the pagination flag: with `f=geojson` the `exceededTransferLimit` marker is nested inside a top-level `properties` object instead of sitting at the root like `f=json`. Miss that and the fetch silently stops after the first 2,000 of 120,913 parcels, which is exactly what the first end-to-end run caught.
- **Messy classifications are real, not hypothetical.** Buda's actual districts include `PD — "Reserve at Cole Springs"` (a name that starts with "R" but isn't residential) and `B2 — "Suburban Residential"` (a business code with a residential name). Both directly stress the brief's classification rule — see §5.
- **Unreliable stated areas + duplicates/nulls.** The parcels feed carries a `legal_acreage` field — the brief says not to trust it, and it's ignored. Ingestion assumes duplicate IDs and null/blank geometries exist and handles (and counts) both.

---

## 2. Architecture

A four-stage pipeline with a hard seam between *raw* and *derived* data, so a re-run — or a corrected source — never leaves a mess.

```mermaid
flowchart LR
    A[ArcGIS REST APIs<br/>Hays Parcels · Buda Zoning] -->|paginated fetch<br/>outSR=2278, f=geojson<br/>CRS verified per page| B[Ingestion<br/>Go]
    B -->|page-by-page upsert<br/>on source_id| C[(Raw Store<br/>raw_parcels · raw_zoning<br/>geometry + source attrs)]
    C -->|stream raw geometry| D[Spatial Processor<br/>Go (orb/planar)]
    D -->|one atomic tx| E[(Derived Model<br/>zoning_districts · parcels<br/>+ assignment + neighborhood)]
    E -->|count + median| F[Stats Query<br/>by neighborhood · by district]
    F --> G[CLI · stdout / JSON]

    subgraph Transform logic
        D1[planar.Area / 43560 → acres]
        D2[PointOnSurface + bbox precheck<br/>+ Contains → district]
        D3[R-code / Residential-name classifier]
    end
```

### System decomposition

| Component | Responsibility |
|---|---|
| **Ingestion (Go)** | Paginated fetch from both ArcGIS services with retries and backoff. Normalizes CRS and format at the boundary and verifies the service honored them. Streams page-by-page into the raw store, so memory stays flat and progress is durable. |
| **Raw Store (SQLite)** | `raw_parcels`, `raw_zoning` — geometry plus *all* source attributes, keyed by source ID. The immutable landing zone; upsert-only, so re-runs and corrected data overwrite instead of duplicating. |
| **Transform (Pure Go + orb)** | Derives acres, classifies residential, assigns each parcel to a zoning district, extracts the neighborhood code. Rebuilds the derived tables in **one transaction** — a failed transform leaves the previous run intact. Counts everything it drops. |
| **Stats / CLI (Go)** | Runs the grouping query and prints the result as JSON. No business logic lives here. |

**Why Pure Go + SQLite.** I explicitly chose *not* to reinvent computational geometry, and also *not* to rely on Docker (PostGIS) or C-compilers (SpatiaLite). To ensure this runs on any machine identically, I used `github.com/paulmach/orb`, a pure Go library that natively handles polygon area computation and Point-in-Polygon raycasting correctly. The data is stored in `modernc.org/sqlite`, a pure Go SQLite port, meaning zero external dependencies are required.

---

## 3. Data model

Two datasets modeled as two entities, joined by an assignment — not flattened into one table, so the relationship between parcel and zoning stays explicit and re-derivable.

```sql
-- Landing zone: raw, keyed by source, geometry + everything the source gave us.
CREATE TABLE raw_parcels (
    source_id TEXT PRIMARY KEY,
    attrs TEXT,      -- full source attributes as JSON; nothing is thrown away
    geom TEXT
);

CREATE TABLE raw_zoning (
    source_id TEXT PRIMARY KEY,
    zone_code TEXT,
    zone_name TEXT,
    geom TEXT
);

-- Derived model: rebuilt from raw on every run.
CREATE TABLE zoning_districts (
    id TEXT PRIMARY KEY,
    zone_code TEXT,
    zone_name TEXT,
    is_residential BOOLEAN
);

CREATE TABLE parcels (
    id TEXT PRIMARY KEY,
    computed_acres REAL,
    district_id TEXT REFERENCES zoning_districts(id),
    neighborhood TEXT   -- hood_cd from the county appraisal data
);
```

Keeping raw geometry and source attributes means the derived layer can be recomputed with corrected logic *without re-fetching* — the whole point of a re-runnable pipeline (`-transform-only` does exactly that).

---

## 4. Key decisions & deliberate deferrals

| Decision | Rationale (one line) |
|---|---|
| Pure Go `orb` over PostGIS/SpatiaLite | Zero dependencies required; no Docker or C-compilers needed on Windows. |
| CLI over UI | A UI is the least-core thing in the brief; the budget goes to data integrity. |
| Normalize both feeds to EPSG:2278 at ingest, verified per page | The county's official zone; one CRS in feet makes area math trivial — and a service that ignores `outSR` fails loudly instead of producing garbage acres. |
| `PointOnSurface` (centroid + scanline fallback) for the join | Guaranteed-interior point; a bare centroid falls outside concave (L/U-shaped) lots and joins them to the wrong district. |
| Bounding-box precheck before point-in-polygon | 120k parcels × 126 districts makes unconditional PIP tests the hot loop. |
| Residential = code starts with "R" *or* name starts with "Residential" | The brief's rule, verbatim; a looser starts-with-R name check would sweep in Buda's `PD — "Reserve at Cole Springs"`. |
| Group primarily by `hood_cd` (appraisal neighborhood), district kept as a second axis | The brief's own example is "per neighborhood," and the county data carries the field; district-only grouping is circular (see §5 Q1). |
| Strictly greater than 1.0 computed acres | "Bigger than an acre" read literally; the boundary case is a stakeholder question, not a guess. |

**Consciously deferred** (named, not hidden):
- **Majority-area overlap join** for parcels straddling a zoning boundary — more accurate, more complex; point-on-surface is the defensible 4-hour call.
- **The UI** — deferred entirely.
- **The 1 km stretch** — deferred; raw geometry is retained, so it's a small addition to the transform loop (planar distance in feet against a given point), not a schema change.
- **Geodesic area** — unnecessary inside a single state-plane zone; distortion is orders of magnitude below the 1-acre threshold.
- **Incremental / resumable fetch** — the fetch is paginated and upserts are idempotent, so an interrupted run re-fetches from zero without harm; true resume tokens weren't worth the budget.

---

## 5. Requirements — assumptions & questions

**Assumptions I made** (explicit, so they can be challenged):

1. **`OBJECTID` is a stable enough key for this exercise.** In production I'd key parcels on `prop_id`/`geo_id` (appraisal identifiers) — `OBJECTID` can be renumbered when the county republishes the layer.
2. **"Bigger than an acre" means strictly `computed_acres > 1.0`**, computed from geometry in EPSG:2278 feet.
3. **Stats are computed over the filtered set** (residential *and* >1 acre), not over all parcels in a group.
4. **Parcels outside any Buda zoning district are excluded from the rollup** — they're the vast majority of the county (see Q4).
5. **Planar area in state-plane feet is accurate enough**; geodesic corrections are noise at this scale.
6. **"Area" means the county appraisal neighborhood (`hood_cd`) for now** — the most literal reading of the brief's "per neighborhood" example that the data actually supports (see Q1).

**Questions I'd take back to stakeholders** (with why they matter):

1. **What does "by area" mean?** The parcels feed carries `hood_cd` (appraisal neighborhood), `abs_subdv_cd` (subdivision), and `situs_city`. I grouped on `hood_cd` because the brief's example literally says "per neighborhood" — but is the *appraisal district's* neighborhood the neighborhood you mean, or do you want subdivisions, council districts, or a spatial grid? *Changes the whole aggregation axis.* (Zoning-district grouping is also reported, but it's mildly circular: the groups are, by construction, residential districts.)
2. **The classification rule pulls in two directions on real data.** As written it *includes* any R-code and *excludes* Buda's actual `B2 — "Suburban Residential"` (business code, residential name — starts with "Suburban", not "Residential"). It would also include codes like `REC` or `ROW` if they existed here. Is over-inclusion on codes and exclusion of residential-named business districts what you want? *Directly moves the counts.*
3. **Straddling parcels.** When a parcel spans two districts, is representative-point assignment acceptable, or do you need majority-area (or split) attribution? *Affects boundary parcels and the definition of "belongs to."*
4. **Unzoned parcels.** Report them as an explicit "outside city / unzoned" bucket, or exclude silently? *Changes whether the output is city-only or county-wide.* (The pipeline counts them — `parcels_unzoned` in the transform stats — it just doesn't put them in the rollup.)
