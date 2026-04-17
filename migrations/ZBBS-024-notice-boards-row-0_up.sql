-- ZBBS-024: Catalog village notice boards row 0, cols 4-8 as prop items
-- 5 variants at (192,0) .. (384,0) on the village notice boards 48x64 sheet.

INSERT INTO asset (id, name, category, default_state, anchor_x, anchor_y, layer, pack_id, source_file) VALUES
    (gen_random_uuid(), 'Notice Board 1', 'prop', 'default', 0.5, 0.85, 'objects', 'mana-seed', 'village notice boards 48x64.png'),
    (gen_random_uuid(), 'Notice Board 2', 'prop', 'default', 0.5, 0.85, 'objects', 'mana-seed', 'village notice boards 48x64.png'),
    (gen_random_uuid(), 'Notice Board 3', 'prop', 'default', 0.5, 0.85, 'objects', 'mana-seed', 'village notice boards 48x64.png'),
    (gen_random_uuid(), 'Notice Board 4', 'prop', 'default', 0.5, 0.85, 'objects', 'mana-seed', 'village notice boards 48x64.png'),
    (gen_random_uuid(), 'Notice Board 5', 'prop', 'default', 0.5, 0.85, 'objects', 'mana-seed', 'village notice boards 48x64.png');

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'default', '/tilesets/mana-seed/village-accessories/village notice boards 48x64.png', 192, 0, 48, 64, 1, 0
FROM asset WHERE name = 'Notice Board 1' AND pack_id = 'mana-seed';

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'default', '/tilesets/mana-seed/village-accessories/village notice boards 48x64.png', 240, 0, 48, 64, 1, 0
FROM asset WHERE name = 'Notice Board 2' AND pack_id = 'mana-seed';

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'default', '/tilesets/mana-seed/village-accessories/village notice boards 48x64.png', 288, 0, 48, 64, 1, 0
FROM asset WHERE name = 'Notice Board 3' AND pack_id = 'mana-seed';

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'default', '/tilesets/mana-seed/village-accessories/village notice boards 48x64.png', 336, 0, 48, 64, 1, 0
FROM asset WHERE name = 'Notice Board 4' AND pack_id = 'mana-seed';

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'default', '/tilesets/mana-seed/village-accessories/village notice boards 48x64.png', 384, 0, 48, 64, 1, 0
FROM asset WHERE name = 'Notice Board 5' AND pack_id = 'mana-seed';
