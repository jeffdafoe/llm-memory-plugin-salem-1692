-- ZBBS-010: Expand asset catalog — new pack metadata, interior flag, granular categories
-- Add all Mana Seed packs and catalog every sprite from every sheet.

-- Pack metadata: group (publisher) and source (marketplace)
ALTER TABLE tileset_pack ADD COLUMN pack_group VARCHAR(60);
ALTER TABLE tileset_pack ADD COLUMN pack_source VARCHAR(200);

-- Update existing mana-seed pack with metadata
UPDATE tileset_pack SET pack_group = 'mana-seed', pack_source = 'https://seliel-the-shaper.itch.io' WHERE id = 'mana-seed';

-- Interior flag — some assets are indoor-only (furniture, fireplaces, etc.)
ALTER TABLE asset ADD COLUMN interior BOOLEAN NOT NULL DEFAULT false;

-- Register individual Mana Seed packs (more specific than the single "mana-seed" entry)
-- The original "mana-seed" entry stays as a catch-all for the summer forest + village accessories.
INSERT INTO tileset_pack (id, name, url, pack_group, pack_source) VALUES
    ('mana-seed-fences', 'Fences & Walls', 'https://seliel-the-shaper.itch.io/mana-seed', 'mana-seed', 'https://seliel-the-shaper.itch.io'),
    ('mana-seed-furnishings', 'Cozy Furnishings', 'https://seliel-the-shaper.itch.io/mana-seed', 'mana-seed', 'https://seliel-the-shaper.itch.io'),
    ('mana-seed-candles', 'Animated Candles', 'https://seliel-the-shaper.itch.io/mana-seed', 'mana-seed', 'https://seliel-the-shaper.itch.io'),
    ('mana-seed-camp', 'Traveler''s Camp', 'https://seliel-the-shaper.itch.io/mana-seed', 'mana-seed', 'https://seliel-the-shaper.itch.io'),
    ('mana-seed-thatch', 'Thatch Roof Home', 'https://seliel-the-shaper.itch.io/mana-seed', 'mana-seed', 'https://seliel-the-shaper.itch.io'),
    ('mana-seed-timber', 'Timber Roof Home', 'https://seliel-the-shaper.itch.io/mana-seed', 'mana-seed', 'https://seliel-the-shaper.itch.io'),
    ('mana-seed-halftimber', 'Half-Timber Home', 'https://seliel-the-shaper.itch.io/mana-seed', 'mana-seed', 'https://seliel-the-shaper.itch.io'),
    ('mana-seed-stonework', 'Stonework Home', 'https://seliel-the-shaper.itch.io/mana-seed', 'mana-seed', 'https://seliel-the-shaper.itch.io'),
    ('mana-seed-npc', 'NPC Pack #1', 'https://seliel-the-shaper.itch.io/mana-seed', 'mana-seed', 'https://seliel-the-shaper.itch.io')
ON CONFLICT (id) DO NOTHING;

-- Also update the original mana-seed entry's group/source
UPDATE tileset_pack SET
    pack_group = 'mana-seed',
    pack_source = 'https://seliel-the-shaper.itch.io'
WHERE id IN ('mana-seed', 'seliel-village', 'mystic-woods', 'rgs-cc0') AND pack_group IS NULL;

-------------------------------------------------------------------
-- NEW ASSETS FROM EXISTING SHEETS (village accessories + summer forest)
-- These were on the sheets but not cataloged in ZBBS-006a
-------------------------------------------------------------------

-- Summer forest: 48x32 sheet — missing bone/antler at position (96,0)
INSERT INTO asset (id, name, category, anchor_x, anchor_y, layer, pack_id) VALUES
    ('bone-pile', 'Bone Pile', 'nature', 0.5, 0.85, 'objects', 'mana-seed');
INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h) VALUES
    ('bone-pile', 'default', '/tilesets/mana-seed/summer-forest/summer sheets/summer 48x32.png', 96, 0, 48, 32);

-- Village accessories 32x32 sheet — missing items (128x160, 4 cols x 5 rows)
-- Row 1 (y=32): (32,32) open basket, (96,32) hay bale
INSERT INTO asset (id, name, category, anchor_x, anchor_y, layer, pack_id) VALUES
    ('basket-open', 'Open Basket', 'prop', 0.5, 0.85, 'objects', 'mana-seed'),
    ('hay-bale', 'Hay Bale', 'prop', 0.5, 0.85, 'objects', 'mana-seed');
INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h) VALUES
    ('basket-open', 'default', '/tilesets/mana-seed/village-accessories/village accessories 32x32.png', 32, 32, 32, 32),
    ('hay-bale', 'default', '/tilesets/mana-seed/village-accessories/village accessories 32x32.png', 96, 32, 32, 32);

-- Row 2 (y=64): hanging beam segments with lanterns/signs/banners
INSERT INTO asset (id, name, category, anchor_x, anchor_y, layer, pack_id) VALUES
    ('beam-lantern-dark', 'Hanging Lantern (Dark)', 'prop', 0.5, 0.5, 'above', 'mana-seed'),
    ('beam-lantern-lit', 'Hanging Lantern (Lit)', 'prop', 0.5, 0.5, 'above', 'mana-seed'),
    ('beam-sign', 'Hanging Sign', 'prop', 0.5, 0.5, 'above', 'mana-seed'),
    ('beam-banner', 'Hanging Banner', 'prop', 0.5, 0.5, 'above', 'mana-seed');
INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h) VALUES
    ('beam-lantern-dark', 'default', '/tilesets/mana-seed/village-accessories/village accessories 32x32.png', 0, 64, 32, 32),
    ('beam-lantern-lit', 'default', '/tilesets/mana-seed/village-accessories/village accessories 32x32.png', 32, 64, 32, 32),
    ('beam-sign', 'default', '/tilesets/mana-seed/village-accessories/village accessories 32x32.png', 64, 64, 32, 32),
    ('beam-banner', 'default', '/tilesets/mana-seed/village-accessories/village accessories 32x32.png', 96, 64, 32, 32);

-- Rows 3-4 (y=96, y=128): storage shelves/racks (8 variants, 4 per row)
INSERT INTO asset (id, name, category, anchor_x, anchor_y, layer, pack_id) VALUES
    ('shelf-1', 'Storage Shelf', 'prop', 0.5, 0.85, 'objects', 'mana-seed'),
    ('shelf-2', 'Storage Shelf (Stocked)', 'prop', 0.5, 0.85, 'objects', 'mana-seed'),
    ('shelf-3', 'Storage Shelf (Full)', 'prop', 0.5, 0.85, 'objects', 'mana-seed'),
    ('shelf-4', 'Storage Shelf (Goods)', 'prop', 0.5, 0.85, 'objects', 'mana-seed'),
    ('shelf-5', 'Log Rack', 'prop', 0.5, 0.85, 'objects', 'mana-seed'),
    ('shelf-6', 'Log Rack (Stocked)', 'prop', 0.5, 0.85, 'objects', 'mana-seed'),
    ('shelf-7', 'Log Rack (Full)', 'prop', 0.5, 0.85, 'objects', 'mana-seed'),
    ('shelf-8', 'Log Rack (Goods)', 'prop', 0.5, 0.85, 'objects', 'mana-seed');
INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h) VALUES
    ('shelf-1', 'default', '/tilesets/mana-seed/village-accessories/village accessories 32x32.png', 0, 96, 32, 32),
    ('shelf-2', 'default', '/tilesets/mana-seed/village-accessories/village accessories 32x32.png', 32, 96, 32, 32),
    ('shelf-3', 'default', '/tilesets/mana-seed/village-accessories/village accessories 32x32.png', 64, 96, 32, 32),
    ('shelf-4', 'default', '/tilesets/mana-seed/village-accessories/village accessories 32x32.png', 96, 96, 32, 32),
    ('shelf-5', 'default', '/tilesets/mana-seed/village-accessories/village accessories 32x32.png', 0, 128, 32, 32),
    ('shelf-6', 'default', '/tilesets/mana-seed/village-accessories/village accessories 32x32.png', 32, 128, 32, 32),
    ('shelf-7', 'default', '/tilesets/mana-seed/village-accessories/village accessories 32x32.png', 64, 128, 32, 32),
    ('shelf-8', 'default', '/tilesets/mana-seed/village-accessories/village accessories 32x32.png', 96, 128, 32, 32);

-------------------------------------------------------------------
-- SUMMER FOREST 16x16 SHEET (96x80 = 6 cols x 5 rows)
-- Mushrooms, leaves, bushes, rocks, ground plants
-------------------------------------------------------------------

INSERT INTO asset (id, name, category, anchor_x, anchor_y, layer, pack_id) VALUES
    -- Row 0: mushrooms
    ('mushroom-grey', 'Grey Mushroom', 'nature', 0.5, 0.85, 'objects', 'mana-seed'),
    ('mushroom-yellow', 'Yellow Mushroom', 'nature', 0.5, 0.85, 'objects', 'mana-seed'),
    ('mushroom-brown', 'Brown Mushroom', 'nature', 0.5, 0.85, 'objects', 'mana-seed'),
    ('mushroom-white', 'White Mushroom', 'nature', 0.5, 0.85, 'objects', 'mana-seed'),
    ('mushroom-layered', 'Layered Mushroom', 'nature', 0.5, 0.85, 'objects', 'mana-seed'),
    ('pinecone', 'Pine Cone', 'nature', 0.5, 0.85, 'objects', 'mana-seed'),
    -- Row 1: leaves and twigs
    ('clover', 'Clover', 'nature', 0.5, 0.85, 'objects', 'mana-seed'),
    ('fern', 'Fern', 'nature', 0.5, 0.85, 'objects', 'mana-seed'),
    ('leaf', 'Leaf', 'nature', 0.5, 0.85, 'objects', 'mana-seed'),
    ('twig', 'Twig', 'nature', 0.5, 0.85, 'objects', 'mana-seed'),
    ('branch-small', 'Small Branch', 'nature', 0.5, 0.85, 'objects', 'mana-seed'),
    ('seeds', 'Seeds', 'nature', 0.5, 0.85, 'objects', 'mana-seed'),
    -- Row 2: small bushes and water plants
    ('bush-round', 'Round Bush', 'nature', 0.5, 0.85, 'objects', 'mana-seed'),
    ('bush-light', 'Light Bush', 'nature', 0.5, 0.85, 'objects', 'mana-seed'),
    ('vine', 'Vine', 'nature', 0.5, 0.85, 'objects', 'mana-seed'),
    ('bush-tiny', 'Tiny Bush', 'nature', 0.5, 0.85, 'objects', 'mana-seed'),
    ('cattails', 'Cattails', 'nature', 0.5, 0.85, 'objects', 'mana-seed'),
    ('water-plant', 'Water Plant', 'nature', 0.5, 0.85, 'objects', 'mana-seed'),
    -- Row 3: rocks
    ('pebble', 'Pebble', 'nature', 0.5, 0.85, 'objects', 'mana-seed'),
    ('rock-tiny', 'Tiny Rock', 'nature', 0.5, 0.85, 'objects', 'mana-seed'),
    ('rock-medium', 'Medium Rocks', 'nature', 0.5, 0.85, 'objects', 'mana-seed'),
    ('rock-large', 'Large Rocks', 'nature', 0.5, 0.85, 'objects', 'mana-seed'),
    ('boulder-pair', 'Boulder Pair', 'nature', 0.5, 0.85, 'objects', 'mana-seed'),
    ('rock-flat', 'Flat Rock', 'nature', 0.5, 0.85, 'objects', 'mana-seed'),
    -- Row 4: ground plants
    ('grass-clump', 'Grass Clump', 'nature', 0.5, 0.85, 'objects', 'mana-seed'),
    ('grass-light', 'Light Grass', 'nature', 0.5, 0.85, 'objects', 'mana-seed');

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h) VALUES
    -- Row 0
    ('mushroom-grey', 'default', '/tilesets/mana-seed/summer-forest/summer sheets/summer 16x16.png', 0, 0, 16, 16),
    ('mushroom-yellow', 'default', '/tilesets/mana-seed/summer-forest/summer sheets/summer 16x16.png', 16, 0, 16, 16),
    ('mushroom-brown', 'default', '/tilesets/mana-seed/summer-forest/summer sheets/summer 16x16.png', 32, 0, 16, 16),
    ('mushroom-white', 'default', '/tilesets/mana-seed/summer-forest/summer sheets/summer 16x16.png', 48, 0, 16, 16),
    ('mushroom-layered', 'default', '/tilesets/mana-seed/summer-forest/summer sheets/summer 16x16.png', 64, 0, 16, 16),
    ('pinecone', 'default', '/tilesets/mana-seed/summer-forest/summer sheets/summer 16x16.png', 80, 0, 16, 16),
    -- Row 1
    ('clover', 'default', '/tilesets/mana-seed/summer-forest/summer sheets/summer 16x16.png', 0, 16, 16, 16),
    ('fern', 'default', '/tilesets/mana-seed/summer-forest/summer sheets/summer 16x16.png', 16, 16, 16, 16),
    ('leaf', 'default', '/tilesets/mana-seed/summer-forest/summer sheets/summer 16x16.png', 32, 16, 16, 16),
    ('twig', 'default', '/tilesets/mana-seed/summer-forest/summer sheets/summer 16x16.png', 48, 16, 16, 16),
    ('branch-small', 'default', '/tilesets/mana-seed/summer-forest/summer sheets/summer 16x16.png', 64, 16, 16, 16),
    ('seeds', 'default', '/tilesets/mana-seed/summer-forest/summer sheets/summer 16x16.png', 80, 16, 16, 16),
    -- Row 2
    ('bush-round', 'default', '/tilesets/mana-seed/summer-forest/summer sheets/summer 16x16.png', 0, 32, 16, 16),
    ('bush-light', 'default', '/tilesets/mana-seed/summer-forest/summer sheets/summer 16x16.png', 16, 32, 16, 16),
    ('vine', 'default', '/tilesets/mana-seed/summer-forest/summer sheets/summer 16x16.png', 32, 32, 16, 16),
    ('bush-tiny', 'default', '/tilesets/mana-seed/summer-forest/summer sheets/summer 16x16.png', 48, 32, 16, 16),
    ('cattails', 'default', '/tilesets/mana-seed/summer-forest/summer sheets/summer 16x16.png', 64, 32, 16, 16),
    ('water-plant', 'default', '/tilesets/mana-seed/summer-forest/summer sheets/summer 16x16.png', 80, 32, 16, 16),
    -- Row 3
    ('pebble', 'default', '/tilesets/mana-seed/summer-forest/summer sheets/summer 16x16.png', 0, 48, 16, 16),
    ('rock-tiny', 'default', '/tilesets/mana-seed/summer-forest/summer sheets/summer 16x16.png', 16, 48, 16, 16),
    ('rock-medium', 'default', '/tilesets/mana-seed/summer-forest/summer sheets/summer 16x16.png', 32, 48, 16, 16),
    ('rock-large', 'default', '/tilesets/mana-seed/summer-forest/summer sheets/summer 16x16.png', 48, 48, 16, 16),
    ('boulder-pair', 'default', '/tilesets/mana-seed/summer-forest/summer sheets/summer 16x16.png', 64, 48, 16, 16),
    ('rock-flat', 'default', '/tilesets/mana-seed/summer-forest/summer sheets/summer 16x16.png', 80, 48, 16, 16),
    -- Row 4
    ('grass-clump', 'default', '/tilesets/mana-seed/summer-forest/summer sheets/summer 16x16.png', 0, 64, 16, 16),
    ('grass-light', 'default', '/tilesets/mana-seed/summer-forest/summer sheets/summer 16x16.png', 16, 64, 16, 16);

-------------------------------------------------------------------
-- SUMMER FOREST 16x32 SHEET (96x32 = 6 cols x 1 row)
-- Tall grass variants and berry bush
-------------------------------------------------------------------

INSERT INTO asset (id, name, category, anchor_x, anchor_y, layer, pack_id) VALUES
    ('grass-low', 'Low Grass', 'nature', 0.5, 0.85, 'objects', 'mana-seed'),
    ('grass-tall', 'Tall Grass', 'nature', 0.5, 0.85, 'objects', 'mana-seed'),
    ('bush-berry-cluster', 'Berry Bush Cluster', 'nature', 0.5, 0.85, 'objects', 'mana-seed');

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h) VALUES
    ('grass-low', 'default', '/tilesets/mana-seed/summer-forest/summer sheets/summer 16x32.png', 0, 0, 16, 32),
    ('grass-tall', 'default', '/tilesets/mana-seed/summer-forest/summer sheets/summer 16x32.png', 16, 0, 16, 32),
    ('bush-berry-cluster', 'default', '/tilesets/mana-seed/summer-forest/summer sheets/summer 16x32.png', 32, 0, 32, 32);

-------------------------------------------------------------------
-- VILLAGE ACCESSORIES 16x16 SHEET (256x64 = 16 cols x 4 rows)
-- Shop signs, pots, flower pots, small props
-------------------------------------------------------------------

INSERT INTO asset (id, name, category, anchor_x, anchor_y, layer, pack_id) VALUES
    -- Row 0: hanging shop signs (16 variants)
    ('sign-sword', 'Shop Sign (Sword)', 'sign', 0.5, 0.5, 'above', 'mana-seed'),
    ('sign-anvil', 'Shop Sign (Anvil)', 'sign', 0.5, 0.5, 'above', 'mana-seed'),
    ('sign-potion', 'Shop Sign (Potion)', 'sign', 0.5, 0.5, 'above', 'mana-seed'),
    ('sign-flower', 'Shop Sign (Flower)', 'sign', 0.5, 0.5, 'above', 'mana-seed'),
    ('sign-star', 'Shop Sign (Star)', 'sign', 0.5, 0.5, 'above', 'mana-seed'),
    ('sign-tools', 'Shop Sign (Tools)', 'sign', 0.5, 0.5, 'above', 'mana-seed'),
    ('sign-bottle', 'Shop Sign (Bottle)', 'sign', 0.5, 0.5, 'above', 'mana-seed'),
    ('sign-scales', 'Shop Sign (Scales)', 'sign', 0.5, 0.5, 'above', 'mana-seed'),
    ('sign-bread', 'Shop Sign (Bread)', 'sign', 0.5, 0.5, 'above', 'mana-seed'),
    ('sign-gem', 'Shop Sign (Gem)', 'sign', 0.5, 0.5, 'above', 'mana-seed'),
    ('sign-scroll', 'Shop Sign (Scroll)', 'sign', 0.5, 0.5, 'above', 'mana-seed'),
    ('sign-shield', 'Shop Sign (Shield)', 'sign', 0.5, 0.5, 'above', 'mana-seed'),
    ('sign-key', 'Shop Sign (Key)', 'sign', 0.5, 0.5, 'above', 'mana-seed'),
    ('sign-barrel', 'Shop Sign (Barrel)', 'sign', 0.5, 0.5, 'above', 'mana-seed'),
    ('sign-blank', 'Shop Sign (Blank)', 'sign', 0.5, 0.5, 'above', 'mana-seed'),
    ('sign-plank', 'Shop Sign (Plank)', 'sign', 0.5, 0.5, 'above', 'mana-seed'),
    -- Row 1: more shop signs
    ('sign-paw', 'Shop Sign (Paw)', 'sign', 0.5, 0.5, 'above', 'mana-seed'),
    ('sign-coffee', 'Shop Sign (Coffee)', 'sign', 0.5, 0.5, 'above', 'mana-seed'),
    ('sign-mushroom', 'Shop Sign (Mushroom)', 'sign', 0.5, 0.5, 'above', 'mana-seed'),
    ('sign-hat', 'Shop Sign (Hat)', 'sign', 0.5, 0.5, 'above', 'mana-seed'),
    ('sign-crossed-swords', 'Shop Sign (Crossed Swords)', 'sign', 0.5, 0.5, 'above', 'mana-seed'),
    ('sign-inn', 'Shop Sign (Inn)', 'sign', 0.5, 0.5, 'above', 'mana-seed'),
    ('sign-percent', 'Shop Sign (Percent)', 'sign', 0.5, 0.5, 'above', 'mana-seed'),
    ('sign-crossed-tools', 'Shop Sign (Crossed Tools)', 'sign', 0.5, 0.5, 'above', 'mana-seed'),
    -- Row 2: small props (not all cells populated)
    ('scroll-item', 'Scroll', 'prop', 0.5, 0.85, 'objects', 'mana-seed'),
    ('pelt', 'Pelt', 'prop', 0.5, 0.85, 'objects', 'mana-seed'),
    ('crystal-ball', 'Crystal Ball', 'prop', 0.5, 0.85, 'objects', 'mana-seed'),
    ('pot-small', 'Small Pot', 'prop', 0.5, 0.85, 'objects', 'mana-seed'),
    ('flower-pot-violet', 'Flower Pot (Violet)', 'prop', 0.5, 0.85, 'objects', 'mana-seed'),
    ('flower-pot-red', 'Flower Pot (Red)', 'prop', 0.5, 0.85, 'objects', 'mana-seed'),
    ('flower-pot-mixed', 'Flower Pot (Mixed)', 'prop', 0.5, 0.85, 'objects', 'mana-seed'),
    ('barrel-small', 'Small Barrel', 'prop', 0.5, 0.85, 'objects', 'mana-seed'),
    ('pot-blue', 'Blue Pot', 'prop', 0.5, 0.85, 'objects', 'mana-seed'),
    ('lantern-item', 'Lantern', 'prop', 0.5, 0.85, 'objects', 'mana-seed'),
    ('money-bag', 'Money Bag', 'prop', 0.5, 0.85, 'objects', 'mana-seed');

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h) VALUES
    -- Row 0
    ('sign-sword', 'default', '/tilesets/mana-seed/village-accessories/village accessories 16x16.png', 0, 0, 16, 16),
    ('sign-anvil', 'default', '/tilesets/mana-seed/village-accessories/village accessories 16x16.png', 16, 0, 16, 16),
    ('sign-potion', 'default', '/tilesets/mana-seed/village-accessories/village accessories 16x16.png', 32, 0, 16, 16),
    ('sign-flower', 'default', '/tilesets/mana-seed/village-accessories/village accessories 16x16.png', 48, 0, 16, 16),
    ('sign-star', 'default', '/tilesets/mana-seed/village-accessories/village accessories 16x16.png', 64, 0, 16, 16),
    ('sign-tools', 'default', '/tilesets/mana-seed/village-accessories/village accessories 16x16.png', 80, 0, 16, 16),
    ('sign-bottle', 'default', '/tilesets/mana-seed/village-accessories/village accessories 16x16.png', 96, 0, 16, 16),
    ('sign-scales', 'default', '/tilesets/mana-seed/village-accessories/village accessories 16x16.png', 112, 0, 16, 16),
    ('sign-bread', 'default', '/tilesets/mana-seed/village-accessories/village accessories 16x16.png', 128, 0, 16, 16),
    ('sign-gem', 'default', '/tilesets/mana-seed/village-accessories/village accessories 16x16.png', 144, 0, 16, 16),
    ('sign-scroll', 'default', '/tilesets/mana-seed/village-accessories/village accessories 16x16.png', 160, 0, 16, 16),
    ('sign-shield', 'default', '/tilesets/mana-seed/village-accessories/village accessories 16x16.png', 176, 0, 16, 16),
    ('sign-key', 'default', '/tilesets/mana-seed/village-accessories/village accessories 16x16.png', 192, 0, 16, 16),
    ('sign-barrel', 'default', '/tilesets/mana-seed/village-accessories/village accessories 16x16.png', 208, 0, 16, 16),
    ('sign-blank', 'default', '/tilesets/mana-seed/village-accessories/village accessories 16x16.png', 224, 0, 16, 16),
    ('sign-plank', 'default', '/tilesets/mana-seed/village-accessories/village accessories 16x16.png', 240, 0, 16, 16),
    -- Row 1
    ('sign-paw', 'default', '/tilesets/mana-seed/village-accessories/village accessories 16x16.png', 0, 16, 16, 16),
    ('sign-coffee', 'default', '/tilesets/mana-seed/village-accessories/village accessories 16x16.png', 16, 16, 16, 16),
    ('sign-mushroom', 'default', '/tilesets/mana-seed/village-accessories/village accessories 16x16.png', 32, 16, 16, 16),
    ('sign-hat', 'default', '/tilesets/mana-seed/village-accessories/village accessories 16x16.png', 48, 16, 16, 16),
    ('sign-crossed-swords', 'default', '/tilesets/mana-seed/village-accessories/village accessories 16x16.png', 64, 16, 16, 16),
    ('sign-inn', 'default', '/tilesets/mana-seed/village-accessories/village accessories 16x16.png', 80, 16, 16, 16),
    ('sign-percent', 'default', '/tilesets/mana-seed/village-accessories/village accessories 16x16.png', 96, 16, 16, 16),
    ('sign-crossed-tools', 'default', '/tilesets/mana-seed/village-accessories/village accessories 16x16.png', 112, 16, 16, 16),
    -- Row 2
    ('scroll-item', 'default', '/tilesets/mana-seed/village-accessories/village accessories 16x16.png', 0, 32, 16, 16),
    ('pelt', 'default', '/tilesets/mana-seed/village-accessories/village accessories 16x16.png', 16, 32, 16, 16),
    ('crystal-ball', 'default', '/tilesets/mana-seed/village-accessories/village accessories 16x16.png', 32, 32, 16, 16),
    ('pot-small', 'default', '/tilesets/mana-seed/village-accessories/village accessories 16x16.png', 48, 32, 16, 16),
    ('flower-pot-violet', 'default', '/tilesets/mana-seed/village-accessories/village accessories 16x16.png', 64, 32, 16, 16),
    ('flower-pot-red', 'default', '/tilesets/mana-seed/village-accessories/village accessories 16x16.png', 80, 32, 16, 16),
    ('flower-pot-mixed', 'default', '/tilesets/mana-seed/village-accessories/village accessories 16x16.png', 96, 32, 16, 16),
    ('barrel-small', 'default', '/tilesets/mana-seed/village-accessories/village accessories 16x16.png', 112, 32, 16, 16),
    ('pot-blue', 'default', '/tilesets/mana-seed/village-accessories/village accessories 16x16.png', 128, 32, 16, 16),
    ('lantern-item', 'default', '/tilesets/mana-seed/village-accessories/village accessories 16x16.png', 144, 32, 16, 16),
    ('money-bag', 'default', '/tilesets/mana-seed/village-accessories/village accessories 16x16.png', 160, 32, 16, 16);

-------------------------------------------------------------------
-- VILLAGE ACCESSORIES 16x48 SHEET (128x48 = 8 cols x 1 row, 3 populated)
-------------------------------------------------------------------

INSERT INTO asset (id, name, category, anchor_x, anchor_y, layer, pack_id) VALUES
    ('potted-cypress', 'Potted Cypress', 'prop', 0.5, 0.9, 'objects', 'mana-seed'),
    ('barrel-stack', 'Barrel Stack', 'prop', 0.5, 0.9, 'objects', 'mana-seed'),
    ('crate-stack', 'Crate Stack', 'prop', 0.5, 0.9, 'objects', 'mana-seed');

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h) VALUES
    ('potted-cypress', 'default', '/tilesets/mana-seed/village-accessories/village accessories 16x48.png', 0, 0, 16, 48),
    ('barrel-stack', 'default', '/tilesets/mana-seed/village-accessories/village accessories 16x48.png', 16, 0, 16, 48),
    ('crate-stack', 'default', '/tilesets/mana-seed/village-accessories/village accessories 16x48.png', 32, 0, 16, 48);

-------------------------------------------------------------------
-- VILLAGE ACCESSORIES 32x64 SHEET (128x192 = 4 cols x 3 rows)
-- Gates and large crates
-------------------------------------------------------------------

INSERT INTO asset (id, name, category, anchor_x, anchor_y, layer, pack_id) VALUES
    ('gate-large', 'Large Gate', 'fence', 0.5, 0.85, 'objects', 'mana-seed'),
    ('poster', 'Posted Notice', 'prop', 0.5, 0.85, 'objects', 'mana-seed'),
    ('crate-large', 'Large Crate', 'prop', 0.5, 0.85, 'objects', 'mana-seed'),
    ('gate-arch', 'Arched Gate', 'fence', 0.5, 0.85, 'objects', 'mana-seed');

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h) VALUES
    ('gate-large', 'closed', '/tilesets/mana-seed/village-accessories/village accessories 32x64.png', 0, 0, 32, 64),
    ('gate-large', 'open', '/tilesets/mana-seed/village-accessories/village accessories 32x64.png', 0, 128, 32, 64),
    ('poster', 'default', '/tilesets/mana-seed/village-accessories/village accessories 32x64.png', 32, 0, 32, 64),
    ('crate-large', 'default', '/tilesets/mana-seed/village-accessories/village accessories 32x64.png', 64, 0, 32, 64),
    ('gate-arch', 'closed', '/tilesets/mana-seed/village-accessories/village accessories 32x64.png', 64, 64, 32, 64),
    ('gate-arch', 'open', '/tilesets/mana-seed/village-accessories/village accessories 32x64.png', 64, 128, 32, 64);

-------------------------------------------------------------------
-- TRAVELER'S CAMP
-------------------------------------------------------------------

-- 32x32 sheet (160x96 = 5 cols x 3 rows)
-- Row 0: unlit fire logs + 4 burning animation frames
-- Row 1: 5 smoking/dying animation frames
-- Row 2: tripod, bedroll closed, bedroll open, cooking spit (food), cooking spit (empty)
INSERT INTO asset (id, name, category, anchor_x, anchor_y, layer, pack_id) VALUES
    ('campfire', 'Campfire', 'camp', 0.5, 0.85, 'objects', 'mana-seed-camp'),
    ('camp-tripod', 'Camp Tripod', 'camp', 0.5, 0.85, 'objects', 'mana-seed-camp'),
    ('bedroll', 'Bedroll', 'camp', 0.5, 0.85, 'objects', 'mana-seed-camp'),
    ('cooking-spit', 'Cooking Spit', 'camp', 0.5, 0.85, 'objects', 'mana-seed-camp');

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h) VALUES
    ('campfire', 'unlit', '/tilesets/mana-seed/travelers-camp/travelers camp 32x32.png', 0, 0, 32, 32),
    ('campfire', 'default', '/tilesets/mana-seed/travelers-camp/travelers camp 32x32.png', 32, 0, 32, 32),
    ('camp-tripod', 'default', '/tilesets/mana-seed/travelers-camp/travelers camp 32x32.png', 0, 64, 32, 32),
    ('bedroll', 'closed', '/tilesets/mana-seed/travelers-camp/travelers camp 32x32.png', 32, 64, 32, 32),
    ('bedroll', 'default', '/tilesets/mana-seed/travelers-camp/travelers camp 32x32.png', 64, 64, 32, 32),
    ('cooking-spit', 'default', '/tilesets/mana-seed/travelers-camp/travelers camp 32x32.png', 96, 64, 32, 32),
    ('cooking-spit', 'empty', '/tilesets/mana-seed/travelers-camp/travelers camp 32x32.png', 128, 64, 32, 32);

-- 64x64 tent sheet (128x64 = 2 tents)
INSERT INTO asset (id, name, category, anchor_x, anchor_y, layer, pack_id) VALUES
    ('tent-open', 'Tent (Open)', 'camp', 0.5, 0.85, 'objects', 'mana-seed-camp'),
    ('tent-closed', 'Tent (Closed)', 'camp', 0.5, 0.85, 'objects', 'mana-seed-camp');

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h) VALUES
    ('tent-open', 'default', '/tilesets/mana-seed/travelers-camp/travelers camp tent 64x64.png', 0, 0, 64, 64),
    ('tent-closed', 'default', '/tilesets/mana-seed/travelers-camp/travelers camp tent 64x64.png', 64, 0, 64, 64);

-- 16x16 tent accessories (48x16 = 3 items)
INSERT INTO asset (id, name, category, anchor_x, anchor_y, layer, pack_id) VALUES
    ('camp-stump', 'Camp Stump', 'camp', 0.5, 0.85, 'objects', 'mana-seed-camp'),
    ('camp-firewood', 'Camp Firewood', 'camp', 0.5, 0.85, 'objects', 'mana-seed-camp'),
    ('camp-sack', 'Camp Sack', 'camp', 0.5, 0.85, 'objects', 'mana-seed-camp');

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h) VALUES
    ('camp-stump', 'default', '/tilesets/mana-seed/travelers-camp/travelers camp tent 16x16.png', 0, 0, 16, 16),
    ('camp-firewood', 'default', '/tilesets/mana-seed/travelers-camp/travelers camp tent 16x16.png', 16, 0, 16, 16),
    ('camp-sack', 'default', '/tilesets/mana-seed/travelers-camp/travelers camp tent 16x16.png', 32, 0, 16, 16);

-------------------------------------------------------------------
-- VILLAGE ACCESSORIES 16x32 SHEET (256x96 = 16 cols x 3 rows)
-- Row 0: 16 heraldic banners/pennants
-- Row 1: lanterns, cabbage, broom, small barrel, then 11 crate variants
-------------------------------------------------------------------

INSERT INTO asset (id, name, category, anchor_x, anchor_y, layer, pack_id) VALUES
    -- Row 0: banners (selecting representative variants, not all 16)
    ('banner-sword', 'Banner (Sword)', 'prop', 0.5, 0.3, 'above', 'mana-seed'),
    ('banner-shield', 'Banner (Shield)', 'prop', 0.5, 0.3, 'above', 'mana-seed'),
    ('banner-crown', 'Banner (Crown)', 'prop', 0.5, 0.3, 'above', 'mana-seed'),
    ('banner-flower', 'Banner (Flower)', 'prop', 0.5, 0.3, 'above', 'mana-seed'),
    ('banner-dragon', 'Banner (Dragon)', 'prop', 0.5, 0.3, 'above', 'mana-seed'),
    ('banner-cross', 'Banner (Cross)', 'prop', 0.5, 0.3, 'above', 'mana-seed'),
    ('banner-anchor', 'Banner (Anchor)', 'prop', 0.5, 0.3, 'above', 'mana-seed'),
    ('banner-plain', 'Banner (Plain)', 'prop', 0.5, 0.3, 'above', 'mana-seed'),
    -- Row 1: misc props
    ('lantern-hanging-dark', 'Hanging Lantern (Dark)', 'prop', 0.5, 0.5, 'above', 'mana-seed'),
    ('lantern-hanging-lit', 'Hanging Lantern (Lit)', 'prop', 0.5, 0.5, 'above', 'mana-seed'),
    ('cabbage', 'Cabbage', 'prop', 0.5, 0.85, 'objects', 'mana-seed'),
    ('broom', 'Broom', 'prop', 0.5, 0.85, 'objects', 'mana-seed'),
    ('barrel-tall', 'Tall Barrel', 'prop', 0.5, 0.85, 'objects', 'mana-seed'),
    ('crate-tall', 'Tall Crate', 'prop', 0.5, 0.85, 'objects', 'mana-seed'),
    ('crate-dark', 'Dark Crate', 'prop', 0.5, 0.85, 'objects', 'mana-seed'),
    ('crate-light', 'Light Crate', 'prop', 0.5, 0.85, 'objects', 'mana-seed'),
    ('crate-straw', 'Straw Crate', 'prop', 0.5, 0.85, 'objects', 'mana-seed'),
    ('hay-stack', 'Hay Stack', 'prop', 0.5, 0.85, 'objects', 'mana-seed');

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h) VALUES
    -- Row 0: banners (every other one for variety)
    ('banner-sword', 'default', '/tilesets/mana-seed/village-accessories/village accessories 16x32.png', 0, 0, 16, 32),
    ('banner-shield', 'default', '/tilesets/mana-seed/village-accessories/village accessories 16x32.png', 32, 0, 16, 32),
    ('banner-crown', 'default', '/tilesets/mana-seed/village-accessories/village accessories 16x32.png', 64, 0, 16, 32),
    ('banner-flower', 'default', '/tilesets/mana-seed/village-accessories/village accessories 16x32.png', 96, 0, 16, 32),
    ('banner-dragon', 'default', '/tilesets/mana-seed/village-accessories/village accessories 16x32.png', 128, 0, 16, 32),
    ('banner-cross', 'default', '/tilesets/mana-seed/village-accessories/village accessories 16x32.png', 160, 0, 16, 32),
    ('banner-anchor', 'default', '/tilesets/mana-seed/village-accessories/village accessories 16x32.png', 192, 0, 16, 32),
    ('banner-plain', 'default', '/tilesets/mana-seed/village-accessories/village accessories 16x32.png', 224, 0, 16, 32),
    -- Row 1
    ('lantern-hanging-dark', 'default', '/tilesets/mana-seed/village-accessories/village accessories 16x32.png', 0, 32, 16, 32),
    ('lantern-hanging-lit', 'default', '/tilesets/mana-seed/village-accessories/village accessories 16x32.png', 16, 32, 16, 32),
    ('cabbage', 'default', '/tilesets/mana-seed/village-accessories/village accessories 16x32.png', 32, 32, 16, 32),
    ('broom', 'default', '/tilesets/mana-seed/village-accessories/village accessories 16x32.png', 48, 32, 16, 32),
    ('barrel-tall', 'default', '/tilesets/mana-seed/village-accessories/village accessories 16x32.png', 64, 32, 16, 32),
    ('crate-tall', 'default', '/tilesets/mana-seed/village-accessories/village accessories 16x32.png', 80, 32, 16, 32),
    ('crate-dark', 'default', '/tilesets/mana-seed/village-accessories/village accessories 16x32.png', 96, 32, 16, 32),
    ('crate-light', 'default', '/tilesets/mana-seed/village-accessories/village accessories 16x32.png', 128, 32, 16, 32),
    ('crate-straw', 'default', '/tilesets/mana-seed/village-accessories/village accessories 16x32.png', 176, 32, 16, 32),
    ('hay-stack', 'default', '/tilesets/mana-seed/village-accessories/village accessories 16x32.png', 240, 32, 16, 32);

-------------------------------------------------------------------
-- FENCE GATES (standalone placeable items from the fences pack)
-- The straight fence segments are tileable and will be handled separately.
-------------------------------------------------------------------

INSERT INTO asset (id, name, category, anchor_x, anchor_y, layer, pack_id) VALUES
    ('gate-ranch', 'Ranch Fence Gate', 'fence', 0.5, 0.85, 'objects', 'mana-seed-fences'),
    ('gate-stone-low', 'Low Stone Gate', 'fence', 0.5, 0.7, 'objects', 'mana-seed-fences'),
    ('gate-iron-stone', 'Iron Stone Gate', 'fence', 0.5, 0.7, 'objects', 'mana-seed-fences'),
    ('gate-stone-high', 'High Stone Gate', 'fence', 0.5, 0.7, 'objects', 'mana-seed-fences'),
    ('door-wrought-iron', 'Wrought Iron Door', 'fence', 0.5, 0.85, 'objects', 'mana-seed-fences');

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h) VALUES
    ('gate-ranch', 'default', '/tilesets/mana-seed/fences-walls/ranch style fence gate 64x48.png', 0, 0, 64, 48),
    ('gate-stone-low', 'closed', '/tilesets/mana-seed/fences-walls/low stone wall gate 64x48.png', 0, 0, 64, 48),
    ('gate-stone-low', 'open', '/tilesets/mana-seed/fences-walls/low stone wall gate 64x48.png', 64, 0, 64, 48),
    ('gate-iron-stone', 'closed', '/tilesets/mana-seed/fences-walls/iron stone wall gate 64x48.png', 0, 0, 64, 48),
    ('gate-iron-stone', 'open', '/tilesets/mana-seed/fences-walls/iron stone wall gate 64x48.png', 64, 0, 64, 48),
    ('gate-stone-high', 'closed', '/tilesets/mana-seed/fences-walls/high stone wall gate 64x64.png', 64, 0, 64, 64),
    ('gate-stone-high', 'open', '/tilesets/mana-seed/fences-walls/high stone wall gate 64x64.png', 128, 0, 64, 64),
    ('door-wrought-iron', 'closed', '/tilesets/mana-seed/fences-walls/door, wrought iron 32x32.png', 0, 0, 32, 31),
    ('door-wrought-iron', 'open', '/tilesets/mana-seed/fences-walls/door, wrought iron 32x32.png', 32, 0, 32, 31);
