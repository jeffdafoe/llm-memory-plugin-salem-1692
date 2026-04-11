-- ZBBS-006: Asset catalog — replaces hardcoded client-side catalog with DB-driven assets
-- Also replaces old village_building tables with general village_object table

-- Drop old building tables (no production data)
DROP TABLE IF EXISTS village_building_resident;
DROP TABLE IF EXISTS village_building;

-- Asset definitions — the logical things (market stall, wagon, maple tree, etc.)
CREATE TABLE asset (
    id VARCHAR(60) NOT NULL,           -- human-readable slug (e.g. "stall-wood", "wagon")
    name VARCHAR(100) NOT NULL,        -- display name (e.g. "Market Stall (Wood)")
    category VARCHAR(30) NOT NULL,     -- tree, nature, structure, prop
    default_state VARCHAR(30) NOT NULL DEFAULT 'default',
    anchor_x DOUBLE PRECISION NOT NULL DEFAULT 0.5,
    anchor_y DOUBLE PRECISION NOT NULL DEFAULT 0.85,
    layer VARCHAR(10) NOT NULL DEFAULT 'objects',  -- objects (behind characters) or above
    created_at TIMESTAMP(0) WITHOUT TIME ZONE NOT NULL DEFAULT NOW(),
    PRIMARY KEY (id)
);

-- Asset states — each visual variant of an asset (open/closed, seasonal, damaged, etc.)
CREATE TABLE asset_state (
    id SERIAL PRIMARY KEY,
    asset_id VARCHAR(60) NOT NULL REFERENCES asset(id) ON DELETE CASCADE,
    state VARCHAR(30) NOT NULL,        -- e.g. "default", "open", "closed"
    sheet VARCHAR(200) NOT NULL,       -- path to spritesheet relative to /assets/
    src_x INT NOT NULL,
    src_y INT NOT NULL,
    src_w INT NOT NULL,
    src_h INT NOT NULL,
    created_at TIMESTAMP(0) WITHOUT TIME ZONE NOT NULL DEFAULT NOW(),
    UNIQUE (asset_id, state)
);
CREATE INDEX idx_asset_state_asset ON asset_state (asset_id);

-- Placed objects on the village map
CREATE TABLE village_object (
    id UUID NOT NULL DEFAULT gen_random_uuid(),
    asset_id VARCHAR(60) NOT NULL REFERENCES asset(id),
    current_state VARCHAR(30) NOT NULL DEFAULT 'default',
    x DOUBLE PRECISION NOT NULL,       -- world pixel position (anchor point)
    y DOUBLE PRECISION NOT NULL,
    placed_by VARCHAR(100),            -- llm-memory agent who placed it (NULL = system)
    owner VARCHAR(100),                -- llm-memory agent who owns it
    created_at TIMESTAMP(0) WITHOUT TIME ZONE NOT NULL DEFAULT NOW(),
    PRIMARY KEY (id)
);
CREATE INDEX idx_village_object_asset ON village_object (asset_id);
CREATE INDEX idx_village_object_owner ON village_object (owner);
