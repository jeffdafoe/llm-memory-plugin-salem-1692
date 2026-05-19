-- ZBBS-008: Add asset catalog system and migrate village_object from catalog_id to asset_id
-- Runs on top of existing ZBBS-006 (old village_object with catalog_id) and ZBBS-007
--
-- Idempotency note (added 2026-05-19): the current ZBBS-006-assets_up.sql
-- has been rewritten retroactively to produce the post-ZBBS-008 schema
-- shape directly (asset / asset_state tables + village_object.asset_id),
-- so a fresh-install application of all migrations would have ZBBS-008
-- re-create tables that ZBBS-006 already made and ALTER columns that
-- already exist. The statements below are gated with IF NOT EXISTS /
-- ON CONFLICT / DO-block existence checks so ZBBS-008 is a no-op against
-- the current ZBBS-006 shape while still doing the right thing if applied
-- against the historical pre-rewrite shape (catalog_id column present).
-- The semantic effect against existing production databases is identical
-- (production-incremental never re-runs ZBBS-008 — the playbook's
-- migrations_applied pre-check filters it out — so this edit changes no
-- behavior in production). The trailing self-INSERT into migrations_applied
-- is removed: the deploy playbook does that insert at deploy.yml:104.

-- 1. Create asset tables
CREATE TABLE IF NOT EXISTS asset (
    id VARCHAR(60) NOT NULL,
    name VARCHAR(100) NOT NULL,
    category VARCHAR(30) NOT NULL,
    default_state VARCHAR(30) NOT NULL DEFAULT 'default',
    anchor_x DOUBLE PRECISION NOT NULL DEFAULT 0.5,
    anchor_y DOUBLE PRECISION NOT NULL DEFAULT 0.85,
    layer VARCHAR(10) NOT NULL DEFAULT 'objects',
    created_at TIMESTAMP(0) WITHOUT TIME ZONE NOT NULL DEFAULT NOW(),
    PRIMARY KEY (id)
);

CREATE TABLE IF NOT EXISTS asset_state (
    id SERIAL PRIMARY KEY,
    asset_id VARCHAR(60) NOT NULL REFERENCES asset(id) ON DELETE CASCADE,
    state VARCHAR(30) NOT NULL,
    sheet VARCHAR(200) NOT NULL,
    src_x INT NOT NULL,
    src_y INT NOT NULL,
    src_w INT NOT NULL,
    src_h INT NOT NULL,
    created_at TIMESTAMP(0) WITHOUT TIME ZONE NOT NULL DEFAULT NOW(),
    UNIQUE (asset_id, state)
);
CREATE INDEX IF NOT EXISTS idx_asset_state_asset ON asset_state (asset_id);

-- 2. Seed asset data (same as ZBBS-006a)
-- Trees
INSERT INTO asset (id, name, category, anchor_x, anchor_y, layer) VALUES
    ('tree-maple', 'Maple Tree', 'tree', 0.5, 0.93, 'objects'),
    ('tree-chestnut', 'Chestnut Tree', 'tree', 0.5, 0.93, 'objects'),
    ('tree-birch', 'Birch Tree', 'tree', 0.5, 0.93, 'objects')
ON CONFLICT (id) DO NOTHING;
INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h) VALUES
    ('tree-maple', 'default', '/assets/tilesets/mana-seed/summer-forest/summer sheets/summer trees 80x112.png', 0, 0, 80, 112),
    ('tree-chestnut', 'default', '/assets/tilesets/mana-seed/summer-forest/summer sheets/summer trees 80x112.png', 80, 0, 80, 112),
    ('tree-birch', 'default', '/assets/tilesets/mana-seed/summer-forest/summer sheets/summer trees 80x112.png', 160, 0, 80, 112)
ON CONFLICT (asset_id, state) DO NOTHING;

-- Small nature
INSERT INTO asset (id, name, category, anchor_x, anchor_y, layer) VALUES
    ('bush', 'Bush', 'nature', 0.5, 0.85, 'objects'),
    ('bush-berries', 'Berry Bush', 'nature', 0.5, 0.85, 'objects'),
    ('rock-small', 'Small Rock', 'nature', 0.5, 0.85, 'objects'),
    ('rock-water', 'River Rock', 'nature', 0.5, 0.85, 'objects'),
    ('stump', 'Tree Stump', 'nature', 0.5, 0.85, 'objects'),
    ('log-pile', 'Log Pile', 'nature', 0.5, 0.85, 'objects'),
    ('bush-small', 'Small Bush', 'nature', 0.5, 0.85, 'objects')
ON CONFLICT (id) DO NOTHING;
INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h) VALUES
    ('bush', 'default', '/assets/tilesets/mana-seed/summer-forest/summer sheets/summer 32x32.png', 0, 0, 32, 32),
    ('bush-berries', 'default', '/assets/tilesets/mana-seed/summer-forest/summer sheets/summer 32x32.png', 32, 0, 32, 32),
    ('rock-small', 'default', '/assets/tilesets/mana-seed/summer-forest/summer sheets/summer 32x32.png', 64, 0, 32, 32),
    ('rock-water', 'default', '/assets/tilesets/mana-seed/summer-forest/summer sheets/summer 32x32.png', 96, 0, 32, 32),
    ('stump', 'default', '/assets/tilesets/mana-seed/summer-forest/summer sheets/summer 32x32.png', 128, 0, 32, 32),
    ('log-pile', 'default', '/assets/tilesets/mana-seed/summer-forest/summer sheets/summer 32x32.png', 160, 0, 32, 32),
    ('bush-small', 'default', '/assets/tilesets/mana-seed/summer-forest/summer sheets/summer 32x32.png', 192, 0, 32, 32)
ON CONFLICT (asset_id, state) DO NOTHING;

-- Medium nature
INSERT INTO asset (id, name, category, anchor_x, anchor_y, layer) VALUES
    ('stump-big', 'Big Stump', 'nature', 0.5, 0.85, 'objects'),
    ('fallen-log', 'Fallen Log', 'nature', 0.5, 0.85, 'objects')
ON CONFLICT (id) DO NOTHING;
INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h) VALUES
    ('stump-big', 'default', '/assets/tilesets/mana-seed/summer-forest/summer sheets/summer 48x32.png', 0, 0, 48, 32),
    ('fallen-log', 'default', '/assets/tilesets/mana-seed/summer-forest/summer sheets/summer 48x32.png', 48, 0, 48, 32)
ON CONFLICT (asset_id, state) DO NOTHING;

-- Bridge
INSERT INTO asset (id, name, category, anchor_x, anchor_y, layer) VALUES
    ('bridge', 'Bridge', 'structure', 0.5, 0.7, 'objects')
ON CONFLICT (id) DO NOTHING;
INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h) VALUES
    ('bridge', 'default', '/assets/tilesets/mana-seed/summer-forest/extras/bonus bridge.png', 0, 0, 64, 48)
ON CONFLICT (asset_id, state) DO NOTHING;

-- Small props
INSERT INTO asset (id, name, category, anchor_x, anchor_y, layer) VALUES
    ('barrel', 'Barrel', 'prop', 0.5, 0.85, 'objects'),
    ('barrel-open', 'Open Barrel', 'prop', 0.5, 0.85, 'objects'),
    ('wood-pile', 'Wood Pile', 'prop', 0.5, 0.85, 'objects'),
    ('wood-shelter', 'Wood Shelter', 'prop', 0.5, 0.85, 'objects'),
    ('crate', 'Crate', 'prop', 0.5, 0.85, 'objects'),
    ('millstone', 'Millstone', 'prop', 0.5, 0.85, 'objects')
ON CONFLICT (id) DO NOTHING;
INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h) VALUES
    ('barrel', 'default', '/assets/tilesets/mana-seed/village-accessories/village accessories 32x32.png', 0, 0, 32, 32),
    ('barrel-open', 'default', '/assets/tilesets/mana-seed/village-accessories/village accessories 32x32.png', 32, 0, 32, 32),
    ('wood-pile', 'default', '/assets/tilesets/mana-seed/village-accessories/village accessories 32x32.png', 64, 0, 32, 32),
    ('wood-shelter', 'default', '/assets/tilesets/mana-seed/village-accessories/village accessories 32x32.png', 96, 0, 32, 32),
    ('crate', 'default', '/assets/tilesets/mana-seed/village-accessories/village accessories 32x32.png', 0, 32, 32, 32),
    ('millstone', 'default', '/assets/tilesets/mana-seed/village-accessories/village accessories 32x32.png', 64, 32, 32, 32)
ON CONFLICT (asset_id, state) DO NOTHING;

-- Wells and large objects
INSERT INTO asset (id, name, category, anchor_x, anchor_y, layer) VALUES
    ('well-empty', 'Well (Empty)', 'structure', 0.5, 0.85, 'objects'),
    ('well-bucket', 'Well (Bucket)', 'structure', 0.5, 0.85, 'objects'),
    ('well-roof', 'Well (Roofed)', 'structure', 0.5, 0.85, 'objects'),
    ('well-wishing', 'Wishing Well', 'structure', 0.5, 0.85, 'objects'),
    ('shop-front', 'Shop Front', 'structure', 0.5, 0.85, 'objects')
ON CONFLICT (id) DO NOTHING;
INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h) VALUES
    ('well-empty', 'default', '/assets/tilesets/mana-seed/village-accessories/village accessories 48x80.png', 0, 0, 48, 80),
    ('well-bucket', 'default', '/assets/tilesets/mana-seed/village-accessories/village accessories 48x80.png', 48, 0, 48, 80),
    ('well-roof', 'default', '/assets/tilesets/mana-seed/village-accessories/village accessories 48x80.png', 96, 0, 48, 80),
    ('well-wishing', 'default', '/assets/tilesets/mana-seed/village-accessories/village accessories 48x80.png', 144, 0, 48, 80),
    ('shop-front', 'default', '/assets/tilesets/mana-seed/village-accessories/village accessories 48x80.png', 192, 0, 48, 80)
ON CONFLICT (asset_id, state) DO NOTHING;

-- Market stalls with open/closed states
INSERT INTO asset (id, name, category, default_state, anchor_x, anchor_y, layer) VALUES
    ('stall-wood', 'Market Stall (Wood)', 'structure', 'open', 0.5, 0.85, 'objects'),
    ('stall-tiled', 'Market Stall (Tiled)', 'structure', 'open', 0.5, 0.85, 'objects'),
    ('stall-fancy', 'Market Stall (Fancy)', 'structure', 'open', 0.5, 0.85, 'objects')
ON CONFLICT (id) DO NOTHING;
INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h) VALUES
    ('stall-wood', 'open', '/assets/tilesets/mana-seed/village-accessories/village accessories 80x96.png', 0, 0, 80, 96),
    ('stall-wood', 'closed', '/assets/tilesets/mana-seed/village-accessories/village accessories 80x96.png', 0, 96, 80, 96),
    ('stall-tiled', 'open', '/assets/tilesets/mana-seed/village-accessories/village accessories 80x96.png', 80, 0, 80, 96),
    ('stall-tiled', 'closed', '/assets/tilesets/mana-seed/village-accessories/village accessories 80x96.png', 80, 96, 80, 96),
    ('stall-fancy', 'open', '/assets/tilesets/mana-seed/village-accessories/village accessories 80x96.png', 160, 0, 80, 96),
    ('stall-fancy', 'closed', '/assets/tilesets/mana-seed/village-accessories/village accessories 80x96.png', 160, 96, 80, 96)
ON CONFLICT (asset_id, state) DO NOTHING;

-- Wagons (corrected srcY: 192)
INSERT INTO asset (id, name, category, anchor_x, anchor_y, layer) VALUES
    ('wagon', 'Wagon', 'structure', 0.5, 0.85, 'objects'),
    ('wagon-covered', 'Covered Wagon', 'structure', 0.5, 0.85, 'objects')
ON CONFLICT (id) DO NOTHING;
INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h) VALUES
    ('wagon', 'default', '/assets/tilesets/mana-seed/village-accessories/village accessories 80x96.png', 80, 192, 80, 96),
    ('wagon-covered', 'default', '/assets/tilesets/mana-seed/village-accessories/village accessories 80x96.png', 160, 192, 80, 96)
ON CONFLICT (asset_id, state) DO NOTHING;

-- Lamp posts
INSERT INTO asset (id, name, category, anchor_x, anchor_y, layer) VALUES
    ('lamppost', 'Lamp Post', 'prop', 0.5, 0.9, 'objects'),
    ('lamppost-sign', 'Lamp Post (Sign)', 'prop', 0.5, 0.9, 'objects'),
    ('lamppost-banner', 'Lamp Post (Banner)', 'prop', 0.5, 0.9, 'objects')
ON CONFLICT (id) DO NOTHING;
INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h) VALUES
    ('lamppost', 'default', '/assets/tilesets/mana-seed/village-accessories/village accessories 48x80.png', 240, 80, 48, 80),
    ('lamppost-sign', 'default', '/assets/tilesets/mana-seed/village-accessories/village accessories 48x80.png', 336, 80, 48, 80),
    ('lamppost-banner', 'default', '/assets/tilesets/mana-seed/village-accessories/village accessories 48x80.png', 432, 80, 48, 80)
ON CONFLICT (asset_id, state) DO NOTHING;

-- 3. Migrate village_object: add asset_id + current_state, populate from catalog_id
-- IF NOT EXISTS gates: on fresh installs ZBBS-006 already created these columns.
ALTER TABLE village_object ADD COLUMN IF NOT EXISTS asset_id VARCHAR(60);
ALTER TABLE village_object ADD COLUMN IF NOT EXISTS current_state VARCHAR(30) NOT NULL DEFAULT 'default';

-- Map existing catalog_ids to asset_ids — only runs against the historical
-- pre-rewrite ZBBS-006 shape that had a catalog_id column. The information_schema
-- guard makes this a no-op against the current ZBBS-006 shape (no catalog_id).
DO $$ BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_name = 'village_object' AND column_name = 'catalog_id'
    ) THEN
        UPDATE village_object SET asset_id = catalog_id;
        -- For any stall-closed-* entries that might exist, map to the base stall with closed state
        UPDATE village_object SET asset_id = 'stall-wood', current_state = 'closed' WHERE catalog_id = 'stall-closed-wood';
        UPDATE village_object SET asset_id = 'stall-tiled', current_state = 'closed' WHERE catalog_id = 'stall-closed-tiled';
        UPDATE village_object SET asset_id = 'stall-fancy', current_state = 'closed' WHERE catalog_id = 'stall-closed-fancy';
        -- Stalls that were "open" (stall-wood, stall-tiled, stall-fancy) get state 'open'
        UPDATE village_object SET current_state = 'open' WHERE asset_id IN ('stall-wood', 'stall-tiled', 'stall-fancy') AND current_state = 'default';
    END IF;
END $$;

-- Now make asset_id NOT NULL and add FK
-- SET NOT NULL is idempotent in pg when the column is already NOT NULL.
ALTER TABLE village_object ALTER COLUMN asset_id SET NOT NULL;

-- ADD CONSTRAINT lacks IF NOT EXISTS in pg 16; gate via pg_constraint lookup.
DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'fk_village_object_asset'
    ) THEN
        ALTER TABLE village_object ADD CONSTRAINT fk_village_object_asset FOREIGN KEY (asset_id) REFERENCES asset(id);
    END IF;
END $$;

-- Drop old catalog_id column and its index — IF EXISTS guards no-op on fresh installs.
DROP INDEX IF EXISTS idx_village_object_catalog;
ALTER TABLE village_object DROP COLUMN IF EXISTS catalog_id;

-- Add new index on asset_id
CREATE INDEX IF NOT EXISTS idx_village_object_asset ON village_object (asset_id);

-- 4. Migration tracking is handled by the deploy playbook (deploy.yml:104).
-- Previous self-INSERT removed; it duplicated the playbook's insert and used
-- the wrong column name (`migration_name` vs. the schema's `migration_id`).
