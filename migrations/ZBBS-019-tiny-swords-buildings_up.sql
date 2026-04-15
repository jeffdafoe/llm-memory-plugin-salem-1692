-- ZBBS-019: Add Tiny Swords building pack (Blue Buildings)
-- Source: Tiny Swords by Pixel Frog (https://pixelfrog-assets.itch.io/tiny-swords)
-- License: free, commercial use OK, credit appreciated

INSERT INTO tileset_pack (id, name, url) VALUES
    ('tiny-swords', 'Tiny Swords', 'https://pixelfrog-assets.itch.io/tiny-swords');

INSERT INTO asset (id, name, category, default_state, anchor_x, anchor_y, layer, pack_id, source_file) VALUES
    (gen_random_uuid(), 'House (Small)',   'structure', 'default', 0.5, 0.9, 'objects', 'tiny-swords', 'House1.png'),
    (gen_random_uuid(), 'House (Medium)',  'structure', 'default', 0.5, 0.9, 'objects', 'tiny-swords', 'House2.png'),
    (gen_random_uuid(), 'House (Tiny)',    'structure', 'default', 0.5, 0.9, 'objects', 'tiny-swords', 'House3.png'),
    (gen_random_uuid(), 'Castle',          'structure', 'default', 0.5, 0.9, 'objects', 'tiny-swords', 'Castle.png'),
    (gen_random_uuid(), 'Tower',           'structure', 'default', 0.5, 0.9, 'objects', 'tiny-swords', 'Tower.png'),
    (gen_random_uuid(), 'Barracks',        'structure', 'default', 0.5, 0.9, 'objects', 'tiny-swords', 'Barracks.png'),
    (gen_random_uuid(), 'Archery Range',   'structure', 'default', 0.5, 0.9, 'objects', 'tiny-swords', 'Archery.png'),
    (gen_random_uuid(), 'Monastery',       'structure', 'default', 0.5, 0.9, 'objects', 'tiny-swords', 'Monastery.png');

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'default', '/tilesets/tiny-swords/buildings/House1.png', 0, 0, 128, 192, 1, 0
FROM asset WHERE name = 'House (Small)' AND pack_id = 'tiny-swords';

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'default', '/tilesets/tiny-swords/buildings/House2.png', 0, 0, 128, 192, 1, 0
FROM asset WHERE name = 'House (Medium)' AND pack_id = 'tiny-swords';

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'default', '/tilesets/tiny-swords/buildings/House3.png', 0, 0, 128, 192, 1, 0
FROM asset WHERE name = 'House (Tiny)' AND pack_id = 'tiny-swords';

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'default', '/tilesets/tiny-swords/buildings/Castle.png', 0, 0, 320, 256, 1, 0
FROM asset WHERE name = 'Castle' AND pack_id = 'tiny-swords';

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'default', '/tilesets/tiny-swords/buildings/Tower.png', 0, 0, 128, 256, 1, 0
FROM asset WHERE name = 'Tower' AND pack_id = 'tiny-swords';

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'default', '/tilesets/tiny-swords/buildings/Barracks.png', 0, 0, 192, 256, 1, 0
FROM asset WHERE name = 'Barracks' AND pack_id = 'tiny-swords';

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'default', '/tilesets/tiny-swords/buildings/Archery.png', 0, 0, 192, 256, 1, 0
FROM asset WHERE name = 'Archery Range' AND pack_id = 'tiny-swords';

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'default', '/tilesets/tiny-swords/buildings/Monastery.png', 0, 0, 192, 320, 1, 0
FROM asset WHERE name = 'Monastery' AND pack_id = 'tiny-swords';
