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

DROP TABLE IF EXISTS parcels;
DROP TABLE IF EXISTS zoning_districts;

CREATE TABLE zoning_districts (
    id TEXT PRIMARY KEY,
    zone_code TEXT,
    zone_name TEXT,
    is_residential BOOLEAN,
    geom TEXT
);

CREATE TABLE parcels (
    id TEXT PRIMARY KEY,
    computed_acres REAL,
    district_id TEXT REFERENCES zoning_districts(id),
    geom TEXT
);
