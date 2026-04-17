-- ZBBS-020: Add Tiny Swords decorations, resources, and nature sprites
-- Source: Tiny Swords by Pixel Frog (https://pixelfrog-assets.itch.io/tiny-swords)
-- License: free, commercial use OK, credit appreciated
--
-- Adds 34 assets: bushes (animated), rocks, water rocks (animated), rubber duck (animated),
-- gold stones/resource, meat, tools, wood, stumps, trees (animated)

INSERT INTO asset (id, name, category, default_state, anchor_x, anchor_y, layer, pack_id, source_file) VALUES
    (gen_random_uuid(), 'Bush 1', 'bush', 'default', 0.5, 0.9, 'objects', 'tiny-swords', 'bush-1.png'),
    (gen_random_uuid(), 'Bush 2', 'bush', 'default', 0.5, 0.9, 'objects', 'tiny-swords', 'bush-2.png'),
    (gen_random_uuid(), 'Bush 3', 'bush', 'default', 0.5, 0.9, 'objects', 'tiny-swords', 'bush-3.png'),
    (gen_random_uuid(), 'Bush 4', 'bush', 'default', 0.5, 0.9, 'objects', 'tiny-swords', 'bush-4.png'),
    (gen_random_uuid(), 'Rock 1', 'nature', 'default', 0.5, 0.9, 'objects', 'tiny-swords', 'rock-1.png'),
    (gen_random_uuid(), 'Rock 2', 'nature', 'default', 0.5, 0.9, 'objects', 'tiny-swords', 'rock-2.png'),
    (gen_random_uuid(), 'Rock 3', 'nature', 'default', 0.5, 0.9, 'objects', 'tiny-swords', 'rock-3.png'),
    (gen_random_uuid(), 'Rock 4', 'nature', 'default', 0.5, 0.9, 'objects', 'tiny-swords', 'rock-4.png'),
    (gen_random_uuid(), 'Water Rock 1', 'nature', 'default', 0.5, 0.7, 'objects', 'tiny-swords', 'water-rock-1.png'),
    (gen_random_uuid(), 'Water Rock 2', 'nature', 'default', 0.5, 0.7, 'objects', 'tiny-swords', 'water-rock-2.png'),
    (gen_random_uuid(), 'Water Rock 3', 'nature', 'default', 0.5, 0.7, 'objects', 'tiny-swords', 'water-rock-3.png'),
    (gen_random_uuid(), 'Water Rock 4', 'nature', 'default', 0.5, 0.7, 'objects', 'tiny-swords', 'water-rock-4.png'),
    (gen_random_uuid(), 'Rubber Duck', 'prop', 'default', 0.5, 0.9, 'objects', 'tiny-swords', 'rubber-duck.png'),
    (gen_random_uuid(), 'Gold Resource', 'prop', 'default', 0.5, 0.9, 'objects', 'tiny-swords', 'gold-resource.png'),
    (gen_random_uuid(), 'Gold Stone 1', 'prop', 'default', 0.5, 0.9, 'objects', 'tiny-swords', 'gold-stone-1.png'),
    (gen_random_uuid(), 'Gold Stone 2', 'prop', 'default', 0.5, 0.9, 'objects', 'tiny-swords', 'gold-stone-2.png'),
    (gen_random_uuid(), 'Gold Stone 3', 'prop', 'default', 0.5, 0.9, 'objects', 'tiny-swords', 'gold-stone-3.png'),
    (gen_random_uuid(), 'Gold Stone 4', 'prop', 'default', 0.5, 0.9, 'objects', 'tiny-swords', 'gold-stone-4.png'),
    (gen_random_uuid(), 'Gold Stone 5', 'prop', 'default', 0.5, 0.9, 'objects', 'tiny-swords', 'gold-stone-5.png'),
    (gen_random_uuid(), 'Gold Stone 6', 'prop', 'default', 0.5, 0.9, 'objects', 'tiny-swords', 'gold-stone-6.png'),
    (gen_random_uuid(), 'Meat Resource', 'prop', 'default', 0.5, 0.9, 'objects', 'tiny-swords', 'meat-resource.png'),
    (gen_random_uuid(), 'Tool 1', 'prop', 'default', 0.5, 0.9, 'objects', 'tiny-swords', 'tool-1.png'),
    (gen_random_uuid(), 'Tool 2', 'prop', 'default', 0.5, 0.9, 'objects', 'tiny-swords', 'tool-2.png'),
    (gen_random_uuid(), 'Tool 3', 'prop', 'default', 0.5, 0.9, 'objects', 'tiny-swords', 'tool-3.png'),
    (gen_random_uuid(), 'Tool 4', 'prop', 'default', 0.5, 0.9, 'objects', 'tiny-swords', 'tool-4.png'),
    (gen_random_uuid(), 'Wood Resource', 'prop', 'default', 0.5, 0.9, 'objects', 'tiny-swords', 'wood-resource.png'),
    (gen_random_uuid(), 'Stump 1', 'stump', 'default', 0.5, 0.9, 'objects', 'tiny-swords', 'stump-1.png'),
    (gen_random_uuid(), 'Stump 2', 'stump', 'default', 0.5, 0.9, 'objects', 'tiny-swords', 'stump-2.png'),
    (gen_random_uuid(), 'Stump 3', 'stump', 'default', 0.5, 0.9, 'objects', 'tiny-swords', 'stump-3.png'),
    (gen_random_uuid(), 'Stump 4', 'stump', 'default', 0.5, 0.9, 'objects', 'tiny-swords', 'stump-4.png'),
    (gen_random_uuid(), 'Tree 1', 'nature', 'default', 0.5, 0.95, 'objects', 'tiny-swords', 'tree-1.png'),
    (gen_random_uuid(), 'Tree 2', 'nature', 'default', 0.5, 0.95, 'objects', 'tiny-swords', 'tree-2.png'),
    (gen_random_uuid(), 'Tree 3', 'nature', 'default', 0.5, 0.95, 'objects', 'tiny-swords', 'tree-3.png'),
    (gen_random_uuid(), 'Tree 4', 'nature', 'default', 0.5, 0.95, 'objects', 'tiny-swords', 'tree-4.png');

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'default', '/tilesets/tiny-swords/decorations/bushes/bush-1.png', 0, 0, 128, 128, 8, 8
FROM asset WHERE source_file = 'bush-1.png' AND pack_id = 'tiny-swords';

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'default', '/tilesets/tiny-swords/decorations/bushes/bush-2.png', 0, 0, 128, 128, 8, 8
FROM asset WHERE source_file = 'bush-2.png' AND pack_id = 'tiny-swords';

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'default', '/tilesets/tiny-swords/decorations/bushes/bush-3.png', 0, 0, 128, 128, 8, 8
FROM asset WHERE source_file = 'bush-3.png' AND pack_id = 'tiny-swords';

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'default', '/tilesets/tiny-swords/decorations/bushes/bush-4.png', 0, 0, 128, 128, 8, 8
FROM asset WHERE source_file = 'bush-4.png' AND pack_id = 'tiny-swords';

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'default', '/tilesets/tiny-swords/decorations/rocks/rock-1.png', 0, 0, 64, 64, 1, 0
FROM asset WHERE source_file = 'rock-1.png' AND pack_id = 'tiny-swords';

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'default', '/tilesets/tiny-swords/decorations/rocks/rock-2.png', 0, 0, 64, 64, 1, 0
FROM asset WHERE source_file = 'rock-2.png' AND pack_id = 'tiny-swords';

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'default', '/tilesets/tiny-swords/decorations/rocks/rock-3.png', 0, 0, 64, 64, 1, 0
FROM asset WHERE source_file = 'rock-3.png' AND pack_id = 'tiny-swords';

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'default', '/tilesets/tiny-swords/decorations/rocks/rock-4.png', 0, 0, 64, 64, 1, 0
FROM asset WHERE source_file = 'rock-4.png' AND pack_id = 'tiny-swords';

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'default', '/tilesets/tiny-swords/decorations/water-rocks/water-rock-1.png', 0, 0, 64, 64, 16, 8
FROM asset WHERE source_file = 'water-rock-1.png' AND pack_id = 'tiny-swords';

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'default', '/tilesets/tiny-swords/decorations/water-rocks/water-rock-2.png', 0, 0, 64, 64, 16, 8
FROM asset WHERE source_file = 'water-rock-2.png' AND pack_id = 'tiny-swords';

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'default', '/tilesets/tiny-swords/decorations/water-rocks/water-rock-3.png', 0, 0, 64, 64, 16, 8
FROM asset WHERE source_file = 'water-rock-3.png' AND pack_id = 'tiny-swords';

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'default', '/tilesets/tiny-swords/decorations/water-rocks/water-rock-4.png', 0, 0, 64, 64, 16, 8
FROM asset WHERE source_file = 'water-rock-4.png' AND pack_id = 'tiny-swords';

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'default', '/tilesets/tiny-swords/decorations/rubber-duck/rubber-duck.png', 0, 0, 32, 32, 3, 4
FROM asset WHERE source_file = 'rubber-duck.png' AND pack_id = 'tiny-swords';

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'default', '/tilesets/tiny-swords/resources/gold-resource/gold-resource.png', 0, 0, 128, 128, 1, 0
FROM asset WHERE source_file = 'gold-resource.png' AND pack_id = 'tiny-swords';

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'default', '/tilesets/tiny-swords/resources/gold-stones/gold-stone-1.png', 0, 0, 128, 128, 1, 0
FROM asset WHERE source_file = 'gold-stone-1.png' AND pack_id = 'tiny-swords';

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'default', '/tilesets/tiny-swords/resources/gold-stones/gold-stone-2.png', 0, 0, 128, 128, 1, 0
FROM asset WHERE source_file = 'gold-stone-2.png' AND pack_id = 'tiny-swords';

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'default', '/tilesets/tiny-swords/resources/gold-stones/gold-stone-3.png', 0, 0, 128, 128, 1, 0
FROM asset WHERE source_file = 'gold-stone-3.png' AND pack_id = 'tiny-swords';

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'default', '/tilesets/tiny-swords/resources/gold-stones/gold-stone-4.png', 0, 0, 128, 128, 1, 0
FROM asset WHERE source_file = 'gold-stone-4.png' AND pack_id = 'tiny-swords';

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'default', '/tilesets/tiny-swords/resources/gold-stones/gold-stone-5.png', 0, 0, 128, 128, 1, 0
FROM asset WHERE source_file = 'gold-stone-5.png' AND pack_id = 'tiny-swords';

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'default', '/tilesets/tiny-swords/resources/gold-stones/gold-stone-6.png', 0, 0, 128, 128, 1, 0
FROM asset WHERE source_file = 'gold-stone-6.png' AND pack_id = 'tiny-swords';

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'default', '/tilesets/tiny-swords/resources/meat/meat-resource.png', 0, 0, 64, 64, 1, 0
FROM asset WHERE source_file = 'meat-resource.png' AND pack_id = 'tiny-swords';

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'default', '/tilesets/tiny-swords/resources/tools/tool-1.png', 0, 0, 64, 64, 1, 0
FROM asset WHERE source_file = 'tool-1.png' AND pack_id = 'tiny-swords';

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'default', '/tilesets/tiny-swords/resources/tools/tool-2.png', 0, 0, 64, 64, 1, 0
FROM asset WHERE source_file = 'tool-2.png' AND pack_id = 'tiny-swords';

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'default', '/tilesets/tiny-swords/resources/tools/tool-3.png', 0, 0, 64, 64, 1, 0
FROM asset WHERE source_file = 'tool-3.png' AND pack_id = 'tiny-swords';

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'default', '/tilesets/tiny-swords/resources/tools/tool-4.png', 0, 0, 64, 64, 1, 0
FROM asset WHERE source_file = 'tool-4.png' AND pack_id = 'tiny-swords';

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'default', '/tilesets/tiny-swords/resources/wood-resource/wood-resource.png', 0, 0, 64, 64, 1, 0
FROM asset WHERE source_file = 'wood-resource.png' AND pack_id = 'tiny-swords';

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'default', '/tilesets/tiny-swords/resources/trees/stump-1.png', 0, 0, 192, 256, 1, 0
FROM asset WHERE source_file = 'stump-1.png' AND pack_id = 'tiny-swords';

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'default', '/tilesets/tiny-swords/resources/trees/stump-2.png', 0, 0, 192, 256, 1, 0
FROM asset WHERE source_file = 'stump-2.png' AND pack_id = 'tiny-swords';

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'default', '/tilesets/tiny-swords/resources/trees/stump-3.png', 0, 0, 192, 256, 1, 0
FROM asset WHERE source_file = 'stump-3.png' AND pack_id = 'tiny-swords';

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'default', '/tilesets/tiny-swords/resources/trees/stump-4.png', 0, 0, 192, 256, 1, 0
FROM asset WHERE source_file = 'stump-4.png' AND pack_id = 'tiny-swords';

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'default', '/tilesets/tiny-swords/resources/trees/tree-1.png', 0, 0, 192, 256, 8, 8
FROM asset WHERE source_file = 'tree-1.png' AND pack_id = 'tiny-swords';

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'default', '/tilesets/tiny-swords/resources/trees/tree-2.png', 0, 0, 192, 256, 8, 8
FROM asset WHERE source_file = 'tree-2.png' AND pack_id = 'tiny-swords';

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'default', '/tilesets/tiny-swords/resources/trees/tree-3.png', 0, 0, 192, 192, 8, 8
FROM asset WHERE source_file = 'tree-3.png' AND pack_id = 'tiny-swords';

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'default', '/tilesets/tiny-swords/resources/trees/tree-4.png', 0, 0, 192, 192, 8, 8
FROM asset WHERE source_file = 'tree-4.png' AND pack_id = 'tiny-swords';

