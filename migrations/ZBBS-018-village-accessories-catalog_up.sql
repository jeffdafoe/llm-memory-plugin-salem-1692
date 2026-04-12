-- ZBBS-018: Village accessories catalog update
-- New entries, slot definitions, and fits_slot assignments for sign overlay system.

-- === NEW ASSETS ===

-- Notice board (row 3, col 4 of notice boards 48x64 sheet)
INSERT INTO asset (id, name, category, default_state, pack_id, source_file)
VALUES (gen_random_uuid(), 'Notice Board', 'prop', 'default', 'mana-seed', 'village notice boards 48x64.png');
INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'default', '/tilesets/mana-seed/village-accessories/village notice boards 48x64.png',
       192, 192, 48, 64, 1, 0
FROM asset WHERE name = 'Notice Board' AND category = 'prop';

-- Signpost A (row 0, col 0 of signposts 48x64 sheet)
INSERT INTO asset (id, name, category, default_state, pack_id, source_file)
VALUES (gen_random_uuid(), 'Signpost (Stone)', 'prop', 'default', 'mana-seed', 'village signposts 48x64.png');
INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'default', '/tilesets/mana-seed/village-accessories/village signposts 48x64.png',
       0, 0, 48, 64, 1, 0
FROM asset WHERE name = 'Signpost (Stone)';

-- Signpost B (row 4, col 0 — that's y=256 on the signposts sheet)
INSERT INTO asset (id, name, category, default_state, pack_id, source_file)
VALUES (gen_random_uuid(), 'Signpost (Wood)', 'prop', 'default', 'mana-seed', 'village signposts 48x64.png');
INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'default', '/tilesets/mana-seed/village-accessories/village signposts 48x64.png',
       0, 256, 48, 64, 1, 0
FROM asset WHERE name = 'Signpost (Wood)';

-- Cart (48x80 row 0, col 5 — x=240, y=0)
INSERT INTO asset (id, name, category, default_state, pack_id, source_file)
VALUES (gen_random_uuid(), 'Cart', 'prop', 'default', 'mana-seed', 'village accessories 48x80.png');
INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'default', '/tilesets/mana-seed/village-accessories/village accessories 48x80.png',
       240, 0, 48, 80, 1, 0
FROM asset WHERE name = 'Cart' AND category = 'prop';

-- Animated torch (anim 16x48 — 5 frames, all cols)
INSERT INTO asset (id, name, category, default_state, pack_id, fits_slot, source_file)
VALUES (gen_random_uuid(), 'Torch', 'prop', 'default', 'mana-seed', 'torch', 'village anim 16x48.png');
INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'default', '/tilesets/mana-seed/village-accessories/village anim 16x48.png',
       0, 0, 16, 48, 5, 8
FROM asset WHERE name = 'Torch';

-- Outhouse (32x64 row 0, col 0)
INSERT INTO asset (id, name, category, default_state, pack_id, source_file)
VALUES (gen_random_uuid(), 'Outhouse', 'structure', 'default', 'mana-seed', 'village accessories 32x64.png');
INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'default', '/tilesets/mana-seed/village-accessories/village accessories 32x64.png',
       0, 0, 32, 64, 1, 0
FROM asset WHERE name = 'Outhouse';

-- Large Crate (32x64 row 0, col 2 — x=64)
INSERT INTO asset (id, name, category, default_state, pack_id, source_file)
VALUES (gen_random_uuid(), 'Large Crate', 'prop', 'default', 'mana-seed', 'village accessories 32x64.png');
INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'default', '/tilesets/mana-seed/village-accessories/village accessories 32x64.png',
       64, 0, 32, 64, 1, 0
FROM asset WHERE name = 'Large Crate' AND source_file = 'village accessories 32x64.png';

-- === SLOT DEFINITIONS ===

-- Notice board gets a sign slot (for posting notices on it)
INSERT INTO asset_slot (asset_id, slot_name, offset_x, offset_y)
SELECT id, 'sign', 0, 0 FROM asset WHERE name = 'Notice Board' AND category = 'prop';

-- Signpost (Stone) gets a sign slot
INSERT INTO asset_slot (asset_id, slot_name, offset_x, offset_y)
SELECT id, 'sign', 0, 0 FROM asset WHERE name = 'Signpost (Stone)';

-- Signpost (Wood) gets a sign slot
INSERT INTO asset_slot (asset_id, slot_name, offset_x, offset_y)
SELECT id, 'sign', 0, 0 FROM asset WHERE name = 'Signpost (Wood)';

-- Existing hanging beam items also get sign slots
INSERT INTO asset_slot (asset_id, slot_name, offset_x, offset_y)
SELECT id, 'sign', 0, 0 FROM asset WHERE name = 'Hanging Sign';
INSERT INTO asset_slot (asset_id, slot_name, offset_x, offset_y)
SELECT id, 'sign', 0, 0 FROM asset WHERE name = 'Hanging Banner';

-- === FITS_SLOT ASSIGNMENTS ===

-- All shop sign icons fit the 'sign' slot
UPDATE asset SET fits_slot = 'sign' WHERE name LIKE 'Shop Sign%';

-- All banners fit the 'sign' slot
UPDATE asset SET fits_slot = 'sign' WHERE name LIKE 'Banner%';
