-- Landing zone: raw, keyed by source, geometry + everything the source gave us.
-- Upsert-only; survives across runs so the derived layer can be recomputed
-- without re-fetching.
CREATE TABLE IF NOT EXISTS raw_parcels (
    source_id TEXT PRIMARY KEY,
    attrs TEXT,
    geom TEXT
);

CREATE TABLE IF NOT EXISTS raw_zoning (
    source_id TEXT PRIMARY KEY,
    zone_code TEXT,
    zone_name TEXT,
    geom TEXT
);

-- Derived model: rebuilt from raw on every run.
DROP TABLE IF EXISTS parcels;
DROP TABLE IF EXISTS zoning_districts;

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
    neighborhood TEXT
);
