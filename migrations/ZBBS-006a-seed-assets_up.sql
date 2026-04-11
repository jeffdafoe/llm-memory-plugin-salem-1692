-- ZBBS-006a: Seed asset catalog with existing objects
-- Maps the old hardcoded catalog.ts entries into the asset/asset_state tables

-- Trees
INSERT INTO asset (id, name, category, anchor_x, anchor_y, layer) VALUES
    ('tree-maple', 'Maple Tree', 'tree', 0.5, 0.93, 'objects'),
    ('tree-chestnut', 'Chestnut Tree', 'tree', 0.5, 0.93, 'objects'),
    ('tree-birch', 'Birch Tree', 'tree', 0.5, 0.93, 'objects');

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h) VALUES
    ('tree-maple', 'default', '/assets/tilesets/mana-seed/summer-forest/summer sheets/summer trees 80x112.png', 0, 0, 80, 112),
    ('tree-chestnut', 'default', '/assets/tilesets/mana-seed/summer-forest/summer sheets/summer trees 80x112.png', 80, 0, 80, 112),
    ('tree-birch', 'default', '/assets/tilesets/mana-seed/summer-forest/summer sheets/summer trees 80x112.png', 160, 0, 80, 112);

-- Small nature (32x32)
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

-- Medium nature (48x32)
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

-- Small props (32x32)
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

-- Wells and large objects (48x80)
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

-- Market stalls — each is ONE asset with open/closed states
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

-- Wagons (corrected srcY: 192, not 288)
INSERT INTO asset (id, name, category, anchor_x, anchor_y, layer) VALUES
    ('wagon', 'Wagon', 'structure', 0.5, 0.85, 'objects'),
    ('wagon-covered', 'Covered Wagon', 'structure', 0.5, 0.85, 'objects');

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h) VALUES
    ('wagon', 'default', '/assets/tilesets/mana-seed/village-accessories/village accessories 80x96.png', 80, 192, 80, 96),
    ('wagon-covered', 'default', '/assets/tilesets/mana-seed/village-accessories/village accessories 80x96.png', 160, 192, 80, 96);

-- Lamp posts (48x80)
INSERT INTO asset (id, name, category, anchor_x, anchor_y, layer) VALUES
    ('lamppost', 'Lamp Post', 'prop', 0.5, 0.9, 'objects'),
    ('lamppost-sign', 'Lamp Post (Sign)', 'prop', 0.5, 0.9, 'objects'),
    ('lamppost-banner', 'Lamp Post (Banner)', 'prop', 0.5, 0.9, 'objects');

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h) VALUES
    ('lamppost', 'default', '/assets/tilesets/mana-seed/village-accessories/village accessories 48x80.png', 240, 80, 48, 80),
    ('lamppost-sign', 'default', '/assets/tilesets/mana-seed/village-accessories/village accessories 48x80.png', 336, 80, 48, 80),
    ('lamppost-banner', 'default', '/assets/tilesets/mana-seed/village-accessories/village accessories 48x80.png', 432, 80, 48, 80);
