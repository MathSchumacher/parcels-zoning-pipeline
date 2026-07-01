# Parcels & Zoning Data Product — Design Document

A pipeline that ingests Hays County parcels and City of Buda zoning, normalizes both, and answers: **residential parcels larger than one acre, with lot size computed from geometry, rolled up into count and median lot size by area.**

Time-boxed to 3–4 hours. This document states what I built, the calls I made, the calls I deliberately deferred, and the questions I'd take back to stakeholders.

---

## 1. Data reconnaissance (what's wrong with the inputs)

A few minutes sizing up the feeds surfaced the issues that actually shaped the design:

- **Two jurisdictions, mismatched coverage.** Zoning covers the *city* of Buda; parcels cover the *whole county*. The overwhelming majority of parcels have no zoning at all — so the spatial join must be built to *not match* as the common case, not the exception.
- **Projection / units quirk.** Area must be computed, not read. Correct area depends entirely on the coordinate system. Hays County sits in the **NAD83 Texas Central** state-plane zone (`EPSG:2277`), whose unit is **US survey feet** — so I normalize both feeds into 2277 and compute area in ft².
- **Serving format.** ArcGIS FeatureServers return Esri JSON (`rings`) by default, where holes and multipart polygons are implied by ring winding rather than structured. Requesting `f=geojson` sidesteps a class of ring-orientation bugs.
- **Unreliable stated areas + likely duplicates/nulls.** The brief warns the stated lot areas are stale; the ingestion assumes duplicate parcel IDs and null/blank zoning codes exist and handles both rather than trusting the source to be clean.

---

## 2. Architecture

A four-stage pipeline with a hard seam between *raw* and *derived* data, so a re-run — or a corrected source — never leaves a mess.

```mermaid
flowchart LR
    A[ArcGIS REST APIs<br/>Hays Parcels · Buda Zoning] -->|paginated fetch<br/>outSR=2277, f=geojson| B[Ingestion<br/>Go]
    B -->|upsert on source_id| C[(Raw Store<br/>raw_parcels · raw_zoning<br/>geometry + source attrs)]
    C -->|stream raw geometry| D[Spatial Processor<br/>Go (orb/planar)]
    D -->|insert transformed| E[(Derived Model<br/>zoning_districts · parcels<br/>+ assignment)]
    E -->|count + median by area| F[Stats Query]
    F --> G[CLI · stdout / JSON]

    subgraph Transform logic
        D1[planar.Area / 43560 → acres]
        D2[PointOnSurface + Contains → district]
        D3[R-prefix classifier on code OR name]
    end
```

### System decomposition

| Component | Responsibility |
|---|---|
| **Ingestion (Go)** | Paginated, resumable fetch from both ArcGIS services. Normalizes CRS and format at the boundary. Owns retries and backoff. |
| **Raw Store (SQLite)** | `raw_parcels`, `raw_zoning` — geometry plus *all* source attributes, keyed by source ID. The immutable landing zone; upsert-only. |
| **Transform (Pure Go + orb)** | Derives acres, classifies residential, assigns each parcel to a zoning district. Uses pure Go spatial math. |
| **Stats / CLI (Go)** | Runs the grouping query and prints the result table (or JSON). No business logic lives here. |

**Why Pure Go + SQLite.** I explicitly chose *not* to reinvent computational geometry, and also *not* to rely on Docker (PostGIS) or C-compilers (SpatiaLite). To ensure this runs on any machine identically, I used `github.com/paulmach/orb`, a pure Go library that natively handles polygon area computation and Point-in-Polygon raycasting correctly. The data is stored in `modernc.org/sqlite`, a pure Go SQLite port, meaning zero external dependencies are required.

---

## 3. Data model

Two datasets modeled as two entities, joined by an assignment — not flattened into one table, so the relationship between parcel and zoning stays explicit and re-derivable.

```sql
-- Landing zone: raw, keyed by source, geometry + everything the source gave us.
CREATE TABLE raw_parcels (
    source_id TEXT PRIMARY KEY,
    attrs TEXT,
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
    district_id TEXT REFERENCES zoning_districts(id)
);
```

Keeping raw geometry and source attributes means the derived layer can be recomputed with corrected logic *without re-fetching* — the whole point of a re-runnable pipeline.

---

## 4. Key decisions & deliberate deferrals

| Decision | Rationale (one line) |
|---|---|
| Pure Go `orb` over PostGIS/SpatiaLite | Zero dependencies required; no Docker or C-compilers needed on Windows. |
| CLI over UI | A UI is the least-core thing in the brief; the budget goes to data integrity. |
| `PointOnSurface` for the join | Guaranteed-interior point; cheap accuracy win over centroid. |
| Normalize both feeds to 2277 at ingest | Single source of truth for units; area math becomes trivial and correct. |
| Group by zoning district (interim) | Directly joinable and defensible — but see the open question below. |

**Consciously deferred** (named, not hidden):
- **Majority-area overlap join** for parcels straddling a zoning boundary — more accurate, more complex; Point-in-Polygon is the defensible 4-hour call.
- **The UI** — deferred entirely.
- **The 1 km stretch** — deferred but *trivial* here: it would require a simple planar distance check inside our `spatial` Go loop.
- **Geodesic area** — unnecessary inside a single state-plane zone.

---

## 5. Requirements — assumptions & questions

**Questions I'd take back to stakeholders** (with why they matter):
1. **What does "by area" mean?** Grouping residential parcels by zoning district is mildly circular (the groups are, by construction, residential districts). Do you want stats by a named **subdivision/neighborhood** field, a **council district**, or a spatial grid (e.g., H3)? *Changes the whole aggregation axis.*
2. **R-prefix false positives.** The rule also matches `REC` (recreation) or `ROW` (right-of-way) if such codes exist. Do we want an explicit exclusion list, or is over-inclusion acceptable? *Directly moves the counts.*
3. **Straddling parcels.** When a parcel spans two districts, is representative-point assignment acceptable, or do you need majority-area (or split) attribution? *Affects boundary parcels and the definition of "belongs to."*
4. **Unzoned parcels.** Report them as an explicit "outside city / unzoned" bucket, or exclude silently? *Changes whether the output is city-only or county-wide.*
