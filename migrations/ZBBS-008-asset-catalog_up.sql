-- ZBBS-008: Add asset catalog system and migrate village_object from catalog_id to asset_id
-- Runs on top of existing ZBBS-006 (old village_object with catalog_id) and ZBBS-007

-- 1. Create asset tables
CREATE TABLE asset (
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

CREATE TABLE asset_state (
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
CREATE INDEX idx_asset_state_asset ON asset_state (asset_id);

-- 2. Seed asset data (same as ZBBS-006a)
-- Trees
INSERT INTO asset (id, name, category, anchor_x, anchor_y, layer) VALUES
    ('tree-maple', 'Maple Tree', 'tree', 0.5, 0.93, 'objects'),
    ('tree-chestnut', 'Chestnut Tree', 'tree', 0.5, 0.93, 'objects'),
    ('tree-birch', 'Birch Tree', 'tree', 0.5, 0.93, 'objects');
INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h) VALUES
    ('tree-maple', 'default', '/assets/tilesets/mana-seed/summer-forest/summer sheets/summer trees 80x112.png', 0, 0, 80, 112),
    ('tree-chestnut', 'default', '/assets/tilesets/mana-seed/summer-forest/summer sheets/summer trees 80x112.png', 80, 0, 80, 112),
    ('tree-birch', 'default', '/assets/tilesets/mana-seed/summer-forest/summer sheets/summer trees 80x112.png', 160, 0, 80, 112);

-- Small nature
INSERT INTO asset (id, name, category, anchor_x, anchor_y, layer) VALUES
    ('bush', 'Bush', 'nature', 0.5, 0.85, 'objects'),
    ('bush-berries', 'Berry Bush', 'nature', 0.5, 0.85, 'objects'),
    ('rock-small', 'Small Rock', 'nature', 0.5, 0.85, 'objects'),
    ('rock-water', 'River Rock', 'nature', 0.5, 0.85, 'objects'),
    ('stump', 'Tree Stump', 'nature', 0.5, 0.85, 'objects'),
    ('log-pile', 'Log Pile', 'nature', 0.5, 0.85, 'objects'),
    ('bush-small', 'Small Bush', 'nature', 0.5, 0.85, 'objects');
INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h) VALUES
    ('bush', 'default', '/assets/tilesets/mana-seed/summer-forest/summer sheets/summer 32x32.png', 0, 0, 32, 32),
    ('bush-berries', 'default', '/assets/tilesets/mana-seed/summer-forest/summer sheets/summer 32x32.png', 32, 0, 32, 32),
    ('rock-small', 'default', '/assets/tilesets/mana-seed/summer-forest/summer sheets/summer 32x32.png', 64, 0, 32, 32),
    ('rock-water', 'default', '/assets/tilesets/mana-seed/summer-forest/summer sheets/summer 32x32.png', 96, 0, 32, 32),
    ('stump', 'default', '/assets/tilesets/mana-seed/summer-forest/summer sheets/summer 32x32.png', 128, 0, 32, 32),
    ('log-pile', 'default', '/assets/tilesets/mana-seed/summer-forest/summer sheets/summer 32x32.png', 160, 0, 32, 32),
    ('bush-small', 'default', '/assets/tilesets/mana-seed/summer-forest/summer sheets/summer 32x32.png', 192, 0, 32, 32);

-- Medium nature
INSERT INTO asset (id, name, category, anchor_x, anchor_y, layer) VALUES
    ('stump-big', 'Big Stump', 'nature', 0.5, 0.85, 'objects'),
    ('fallen-log', 'Fallen Log', 'nature', 0.5, 0.85, 'objects');
INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h) VALUES
    ('stump-big', 'default', '/assets/tilesets/mana-seed/summer-forest/summer sheets/summer 48x32.png', 0, 0, 48, 32),
    ('fallen-log', 'default', '/assets/tilesets/mana-seed/summer-forest/summer sheets/summer 48x32.png', 48, 0, 48, 32);

-- Bridge
INSERT INTO asset (id, name, category, anchor_x, anchor_y, layer) VALUES
    ('bridge', 'Bridge', 'structure', 0.5, 0.7, 'objects');
INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h) VALUES
    ('bridge', 'default', '/assets/tilesets/mana-seed/summer-forest/extras/bonus bridge.png', 0, 0, 64, 48);

-- Small props
INSERT INTO asset (id, name, category, anchor_x, anchor_y, layer) VALUES
    ('barrel', 'Barrel', 'prop', 0.5, 0.85, 'objects'),
    ('barrel-open', 'Open Barrel', 'prop', 0.5, 0.85, 'objects'),
    ('wood-pile', 'Wood Pile', 'prop', 0.5, 0.85, 'objects'),
    ('wood-shelter', 'Wood Shelter', 'prop', 0.5, 0.85, 'objects'),
    ('crate', 'Crate', 'prop', 0.5, 0.85, 'objects'),
    ('millstone', 'Millstone', 'prop', 0.5, 0.85, 'objects');
INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h) VALUES
    ('barrel', 'default', '/assets/tilesets/mana-seed/village-accessories/village accessories 32x32.png', 0, 0, 32, 32),
    ('barrel-open', 'default', '/assets/tilesets/mana-seed/village-accessories/village accessories 32x32.png', 32, 0, 32, 32),
    ('wood-pile', 'default', '/assets/tilesets/mana-seed/village-accessories/village accessories 32x32.png', 64, 0, 32, 32),
    ('wood-shelter', 'default', '/assets/tilesets/mana-seed/village-accessories/village accessories 32x32.png', 96, 0, 32, 32),
    ('crate', 'default', '/assets/tilesets/mana-seed/village-accessories/village accessories 32x32.png', 0, 32, 32, 32),
    ('millstone', 'default', '/assets/tilesets/mana-seed/village-accessories/village accessories 32x32.png', 64, 32, 32, 32);

-- Wells and large objects
INSERT INTO asset (id, name, category, anchor_x, anchor_y, layer) VALUES
    ('well-empty', 'Well (Empty)', 'structure', 0.5, 0.85, 'objects'),
    ('well-bucket', 'Well (Bucket)', 'structure', 0.5, 0.85, 'objects'),
    ('well-roof', 'Well (Roofed)', 'structure', 0.5, 0.85, 'objects'),
    ('well-wishing', 'Wishing Well', 'structure', 0.5, 0.85, 'objects'),
    ('shop-front', 'Shop Front', 'structure', 0.5, 0.85, 'objects');
INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h) VALUES
    ('well-empty', 'default', '/assets/tilesets/mana-seed/village-accessories/village accessories 48x80.png', 0, 0, 48, 80),
    ('well-bucket', 'default', '/assets/tilesets/mana-seed/village-accessories/village accessories 48x80.png', 48, 0, 48, 80),
    ('well-roof', 'default', '/assets/tilesets/mana-seed/village-accessories/village accessories 48x80.png', 96, 0, 48, 80),
    ('well-wishing', 'default', '/assets/tilesets/mana-seed/village-accessories/village accessories 48x80.png', 144, 0, 48, 80),
    ('shop-front', 'default', '/assets/tilesets/mana-seed/village-accessories/village accessories 48x80.png', 192, 0, 48, 80);

-- Market stalls with open/closed states
INSERT INTO asset (id, name, category, default_state, anchor_x, anchor_y, layer) VALUES
    ('stall-wood', 'Market Stall (Wood)', 'structure', 'open', 0.5, 0.85, 'objects'),
    ('stall-tiled', 'Market Stall (Tiled)', 'structure', 'open', 0.5, 0.85, 'objects'),
    ('stall-fancy', 'Market Stall (Fancy)', 'structure', 'open', 0.5, 0.85, 'objects');
INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h) VALUES
    ('stall-wood', 'open', '/assets/tilesets/mana-seed/village-accessories/village accessories 80x96.png', 0, 0, 80, 96),
    ('stall-wood', 'closed', '/assets/tilesets/mana-seed/village-accessories/village accessories 80x96.png', 0, 96, 80, 96),
    ('stall-tiled', 'open', '/assets/tilesets/mana-seed/village-accessories/village accessories 80x96.png', 80, 0, 80, 96),
    ('stall-tiled', 'closed', '/assets/tilesets/mana-seed/village-accessories/village accessories 80x96.png', 80, 96, 80, 96),
    ('stall-fancy', 'open', '/assets/tilesets/mana-seed/village-accessories/village accessories 80x96.png', 160, 0, 80, 96),
    ('stall-fancy', 'closed', '/assets/tilesets/mana-seed/village-accessories/village accessories 80x96.png', 160, 96, 80, 96);

-- Wagons (corrected srcY: 192)
INSERT INTO asset (id, name, category, anchor_x, anchor_y, layer) VALUES
    ('wagon', 'Wagon', 'structure', 0.5, 0.85, 'objects'),
    ('wagon-covered', 'Covered Wagon', 'structure', 0.5, 0.85, 'objects');
INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h) VALUES
    ('wagon', 'default', '/assets/tilesets/mana-seed/village-accessories/village accessories 80x96.png', 80, 192, 80, 96),
    ('wagon-covered', 'default', '/assets/tilesets/mana-seed/village-accessories/village accessories 80x96.png', 160, 192, 80, 96);

-- Lamp posts
INSERT INTO asset (id, name, category, anchor_x, anchor_y, layer) VALUES
    ('lamppost', 'Lamp Post', 'prop', 0.5, 0.9, 'objects'),
    ('lamppost-sign', 'Lamp Post (Sign)', 'prop', 0.5, 0.9, 'objects'),
    ('lamppost-banner', 'Lamp Post (Banner)', 'prop', 0.5, 0.9, 'objects');
INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h) VALUES
    ('lamppost', 'default', '/assets/tilesets/mana-seed/village-accessories/village accessories 48x80.png', 240, 80, 48, 80),
    ('lamppost-sign', 'default', '/assets/tilesets/mana-seed/village-accessories/village accessories 48x80.png', 336, 80, 48, 80),
    ('lamppost-banner', 'default', '/assets/tilesets/mana-seed/village-accessories/village accessories 48x80.png', 432, 80, 48, 80);

-- 3. Migrate village_object: add asset_id + current_state, populate from catalog_id
ALTER TABLE village_object ADD COLUMN asset_id VARCHAR(60);
ALTER TABLE village_object ADD COLUMN current_state VARCHAR(30) NOT NULL DEFAULT 'default';

-- Map existing catalog_ids to asset_ids (all are 1:1 except stall-closed-* which don't exist in data)
UPDATE village_object SET asset_id = catalog_id;

-- For any stall-closed-* entries that might exist, map to the base stall with closed state
UPDATE village_object SET asset_id = 'stall-wood', current_state = 'closed' WHERE catalog_id = 'stall-closed-wood';
UPDATE village_object SET asset_id = 'stall-tiled', current_state = 'closed' WHERE catalog_id = 'stall-closed-tiled';
UPDATE village_object SET asset_id = 'stall-fancy', current_state = 'closed' WHERE catalog_id = 'stall-closed-fancy';

-- Stalls that were "open" (stall-wood, stall-tiled, stall-fancy) get state 'open'
UPDATE village_object SET current_state = 'open' WHERE asset_id IN ('stall-wood', 'stall-tiled', 'stall-fancy') AND current_state = 'default';

-- Now make asset_id NOT NULL and add FK
ALTER TABLE village_object ALTER COLUMN asset_id SET NOT NULL;
ALTER TABLE village_object ADD CONSTRAINT fk_village_object_asset FOREIGN KEY (asset_id) REFERENCES asset(id);

-- Drop old catalog_id column and its index
DROP INDEX IF EXISTS idx_village_object_catalog;
ALTER TABLE village_object DROP COLUMN catalog_id;

-- Add new index on asset_id
CREATE INDEX idx_village_object_asset ON village_object (asset_id);

-- 4. Record migration
INSERT INTO migrations_applied (migration_name) VALUES ('ZBBS-008-asset-catalog');
