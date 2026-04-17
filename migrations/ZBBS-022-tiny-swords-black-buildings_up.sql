-- ZBBS-022: Add Black Tiny Swords buildings
-- Source: Tiny Swords by Pixel Frog (https://pixelfrog-assets.itch.io/tiny-swords)
-- Follows the color-subdir pattern established in ZBBS-021. Purple still deferred.

INSERT INTO asset (id, name, category, default_state, anchor_x, anchor_y, layer, pack_id, source_file) VALUES
    (gen_random_uuid(), 'Black House (Small)',   'structure', 'default', 0.5, 0.9, 'objects', 'tiny-swords', 'black/House1.png'),
    (gen_random_uuid(), 'Black House (Medium)',  'structure', 'default', 0.5, 0.9, 'objects', 'tiny-swords', 'black/House2.png'),
    (gen_random_uuid(), 'Black House (Tiny)',    'structure', 'default', 0.5, 0.9, 'objects', 'tiny-swords', 'black/House3.png'),
    (gen_random_uuid(), 'Black Castle',          'structure', 'default', 0.5, 0.9, 'objects', 'tiny-swords', 'black/Castle.png'),
    (gen_random_uuid(), 'Black Tower',           'structure', 'default', 0.5, 0.9, 'objects', 'tiny-swords', 'black/Tower.png'),
    (gen_random_uuid(), 'Black Barracks',        'structure', 'default', 0.5, 0.9, 'objects', 'tiny-swords', 'black/Barracks.png'),
    (gen_random_uuid(), 'Black Archery Range',   'structure', 'default', 0.5, 0.9, 'objects', 'tiny-swords', 'black/Archery.png'),
    (gen_random_uuid(), 'Black Monastery',       'structure', 'default', 0.5, 0.9, 'objects', 'tiny-swords', 'black/Monastery.png');

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'default', '/tilesets/tiny-swords/buildings/black/House1.png', 0, 0, 128, 192, 1, 0
FROM asset WHERE name = 'Black House (Small)' AND pack_id = 'tiny-swords';

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'default', '/tilesets/tiny-swords/buildings/black/House2.png', 0, 0, 128, 192, 1, 0
FROM asset WHERE name = 'Black House (Medium)' AND pack_id = 'tiny-swords';

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'default', '/tilesets/tiny-swords/buildings/black/House3.png', 0, 0, 128, 192, 1, 0
FROM asset WHERE name = 'Black House (Tiny)' AND pack_id = 'tiny-swords';

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'default', '/tilesets/tiny-swords/buildings/black/Castle.png', 0, 0, 320, 256, 1, 0
FROM asset WHERE name = 'Black Castle' AND pack_id = 'tiny-swords';

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'default', '/tilesets/tiny-swords/buildings/black/Tower.png', 0, 0, 128, 256, 1, 0
FROM asset WHERE name = 'Black Tower' AND pack_id = 'tiny-swords';

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'default', '/tilesets/tiny-swords/buildings/black/Barracks.png', 0, 0, 192, 256, 1, 0
FROM asset WHERE name = 'Black Barracks' AND pack_id = 'tiny-swords';

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'default', '/tilesets/tiny-swords/buildings/black/Archery.png', 0, 0, 192, 256, 1, 0
FROM asset WHERE name = 'Black Archery Range' AND pack_id = 'tiny-swords';

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'default', '/tilesets/tiny-swords/buildings/black/Monastery.png', 0, 0, 192, 320, 1, 0
FROM asset WHERE name = 'Black Monastery' AND pack_id = 'tiny-swords';
