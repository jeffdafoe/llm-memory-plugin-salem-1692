-- ZBBS-021: Add Red + Yellow Tiny Swords buildings; reorganize blue into color subdir
-- Source: Tiny Swords by Pixel Frog (https://pixelfrog-assets.itch.io/tiny-swords)
-- Blue PNGs are moved from /buildings/*.png to /buildings/blue/*.png
-- Display names get color prefix so the editor sidebar groups by color alphabetically

-- Rename existing blue rows: prepend "Blue " to name, prefix source_file with blue/
UPDATE asset SET
    name = 'Blue ' || name,
    source_file = 'blue/' || source_file
WHERE pack_id = 'tiny-swords'
  AND name IN ('House (Small)', 'House (Medium)', 'House (Tiny)',
               'Castle', 'Tower', 'Barracks', 'Archery Range', 'Monastery');

-- Relocate existing blue asset_state sheet paths into blue/ subdir
UPDATE asset_state
SET sheet = REPLACE(sheet, '/tilesets/tiny-swords/buildings/', '/tilesets/tiny-swords/buildings/blue/')
WHERE sheet LIKE '/tilesets/tiny-swords/buildings/%.png';

-- Red buildings
INSERT INTO asset (id, name, category, default_state, anchor_x, anchor_y, layer, pack_id, source_file) VALUES
    (gen_random_uuid(), 'Red House (Small)',   'structure', 'default', 0.5, 0.9, 'objects', 'tiny-swords', 'red/House1.png'),
    (gen_random_uuid(), 'Red House (Medium)',  'structure', 'default', 0.5, 0.9, 'objects', 'tiny-swords', 'red/House2.png'),
    (gen_random_uuid(), 'Red House (Tiny)',    'structure', 'default', 0.5, 0.9, 'objects', 'tiny-swords', 'red/House3.png'),
    (gen_random_uuid(), 'Red Castle',          'structure', 'default', 0.5, 0.9, 'objects', 'tiny-swords', 'red/Castle.png'),
    (gen_random_uuid(), 'Red Tower',           'structure', 'default', 0.5, 0.9, 'objects', 'tiny-swords', 'red/Tower.png'),
    (gen_random_uuid(), 'Red Barracks',        'structure', 'default', 0.5, 0.9, 'objects', 'tiny-swords', 'red/Barracks.png'),
    (gen_random_uuid(), 'Red Archery Range',   'structure', 'default', 0.5, 0.9, 'objects', 'tiny-swords', 'red/Archery.png'),
    (gen_random_uuid(), 'Red Monastery',       'structure', 'default', 0.5, 0.9, 'objects', 'tiny-swords', 'red/Monastery.png');

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'default', '/tilesets/tiny-swords/buildings/red/House1.png', 0, 0, 128, 192, 1, 0
FROM asset WHERE name = 'Red House (Small)' AND pack_id = 'tiny-swords';

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'default', '/tilesets/tiny-swords/buildings/red/House2.png', 0, 0, 128, 192, 1, 0
FROM asset WHERE name = 'Red House (Medium)' AND pack_id = 'tiny-swords';

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'default', '/tilesets/tiny-swords/buildings/red/House3.png', 0, 0, 128, 192, 1, 0
FROM asset WHERE name = 'Red House (Tiny)' AND pack_id = 'tiny-swords';

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'default', '/tilesets/tiny-swords/buildings/red/Castle.png', 0, 0, 320, 256, 1, 0
FROM asset WHERE name = 'Red Castle' AND pack_id = 'tiny-swords';

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'default', '/tilesets/tiny-swords/buildings/red/Tower.png', 0, 0, 128, 256, 1, 0
FROM asset WHERE name = 'Red Tower' AND pack_id = 'tiny-swords';

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'default', '/tilesets/tiny-swords/buildings/red/Barracks.png', 0, 0, 192, 256, 1, 0
FROM asset WHERE name = 'Red Barracks' AND pack_id = 'tiny-swords';

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'default', '/tilesets/tiny-swords/buildings/red/Archery.png', 0, 0, 192, 256, 1, 0
FROM asset WHERE name = 'Red Archery Range' AND pack_id = 'tiny-swords';

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'default', '/tilesets/tiny-swords/buildings/red/Monastery.png', 0, 0, 192, 320, 1, 0
FROM asset WHERE name = 'Red Monastery' AND pack_id = 'tiny-swords';

-- Yellow buildings
INSERT INTO asset (id, name, category, default_state, anchor_x, anchor_y, layer, pack_id, source_file) VALUES
    (gen_random_uuid(), 'Yellow House (Small)',   'structure', 'default', 0.5, 0.9, 'objects', 'tiny-swords', 'yellow/House1.png'),
    (gen_random_uuid(), 'Yellow House (Medium)',  'structure', 'default', 0.5, 0.9, 'objects', 'tiny-swords', 'yellow/House2.png'),
    (gen_random_uuid(), 'Yellow House (Tiny)',    'structure', 'default', 0.5, 0.9, 'objects', 'tiny-swords', 'yellow/House3.png'),
    (gen_random_uuid(), 'Yellow Castle',          'structure', 'default', 0.5, 0.9, 'objects', 'tiny-swords', 'yellow/Castle.png'),
    (gen_random_uuid(), 'Yellow Tower',           'structure', 'default', 0.5, 0.9, 'objects', 'tiny-swords', 'yellow/Tower.png'),
    (gen_random_uuid(), 'Yellow Barracks',        'structure', 'default', 0.5, 0.9, 'objects', 'tiny-swords', 'yellow/Barracks.png'),
    (gen_random_uuid(), 'Yellow Archery Range',   'structure', 'default', 0.5, 0.9, 'objects', 'tiny-swords', 'yellow/Archery.png'),
    (gen_random_uuid(), 'Yellow Monastery',       'structure', 'default', 0.5, 0.9, 'objects', 'tiny-swords', 'yellow/Monastery.png');

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'default', '/tilesets/tiny-swords/buildings/yellow/House1.png', 0, 0, 128, 192, 1, 0
FROM asset WHERE name = 'Yellow House (Small)' AND pack_id = 'tiny-swords';

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'default', '/tilesets/tiny-swords/buildings/yellow/House2.png', 0, 0, 128, 192, 1, 0
FROM asset WHERE name = 'Yellow House (Medium)' AND pack_id = 'tiny-swords';

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'default', '/tilesets/tiny-swords/buildings/yellow/House3.png', 0, 0, 128, 192, 1, 0
FROM asset WHERE name = 'Yellow House (Tiny)' AND pack_id = 'tiny-swords';

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'default', '/tilesets/tiny-swords/buildings/yellow/Castle.png', 0, 0, 320, 256, 1, 0
FROM asset WHERE name = 'Yellow Castle' AND pack_id = 'tiny-swords';

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'default', '/tilesets/tiny-swords/buildings/yellow/Tower.png', 0, 0, 128, 256, 1, 0
FROM asset WHERE name = 'Yellow Tower' AND pack_id = 'tiny-swords';

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'default', '/tilesets/tiny-swords/buildings/yellow/Barracks.png', 0, 0, 192, 256, 1, 0
FROM asset WHERE name = 'Yellow Barracks' AND pack_id = 'tiny-swords';

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'default', '/tilesets/tiny-swords/buildings/yellow/Archery.png', 0, 0, 192, 256, 1, 0
FROM asset WHERE name = 'Yellow Archery Range' AND pack_id = 'tiny-swords';

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'default', '/tilesets/tiny-swords/buildings/yellow/Monastery.png', 0, 0, 192, 320, 1, 0
FROM asset WHERE name = 'Yellow Monastery' AND pack_id = 'tiny-swords';
