-- Terrain grid storage — stores the entire map terrain as a flat byte array.
-- Single row (id=1) since there's only one map.
-- Each byte is a terrain type index (1-6): dirt, light grass, dark grass,
-- cobblestone, shallow water, deep water.

CREATE TABLE village_terrain (
    id INTEGER PRIMARY KEY DEFAULT 1,
    width INTEGER NOT NULL,
    height INTEGER NOT NULL,
    data BYTEA NOT NULL,
    updated_by VARCHAR(100),
    updated_at TIMESTAMP(0) WITHOUT TIME ZONE NOT NULL DEFAULT NOW(),
    CONSTRAINT single_terrain CHECK (id = 1)
);
