-- ZBBS-006: Village objects — replaces village_building with a general object table

-- Drop old building tables (no production data yet)
DROP TABLE IF EXISTS village_building_resident;
DROP TABLE IF EXISTS village_building;

-- All placed items on the map (trees, rocks, buildings, stalls, props)
CREATE TABLE village_object (
    id UUID NOT NULL DEFAULT gen_random_uuid(),
    catalog_id VARCHAR(60) NOT NULL,     -- references the client-side catalog item ID
    x DOUBLE PRECISION NOT NULL,         -- world pixel position (anchor point)
    y DOUBLE PRECISION NOT NULL,
    placed_by VARCHAR(100),              -- llm-memory agent who placed it (NULL = system)
    owner VARCHAR(100),                  -- llm-memory agent who owns it (for buildings/stalls)
    created_at TIMESTAMP(0) WITHOUT TIME ZONE NOT NULL DEFAULT NOW(),
    PRIMARY KEY (id)
);
CREATE INDEX idx_village_object_catalog ON village_object (catalog_id);
CREATE INDEX idx_village_object_owner ON village_object (owner);
