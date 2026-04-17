-- ZBBS-029: Collapse Log Rack progression into one asset with 4 fill states.
--
-- Row 4 cols 0-3 of village accessories 32x32.png are a fill progression:
--   (0,128) empty  (32,128) low  (64,128) mid  (96,128) full
-- These were modeled as 4 separate assets (Log Rack, Log Rack (Stocked),
-- Log Rack (Full), Log Rack (Goods)). Collapse into one asset with states.
--
-- No tags yet — fill level is a gameplay signal (woodcutter adds, fire consumes).
-- Add a tag when we know the scheduler's hook. States are named 'empty', 'low',
-- 'mid', 'full' rather than level-0..3 for readability.
--
-- Note: Storage Shelf row 3 is the same pattern and should get the same
-- treatment in a follow-up migration.

-- All 4 Log Rack* rows have 0 placements; safe to delete.
DELETE FROM asset_state
WHERE asset_id IN (SELECT id FROM asset WHERE name LIKE 'Log Rack%');

DELETE FROM asset
WHERE name LIKE 'Log Rack%';

INSERT INTO asset (id, name, category, default_state, anchor_x, anchor_y, layer, pack_id, source_file) VALUES
    (gen_random_uuid(), 'Log Rack', 'prop', 'empty', 0.5, 0.85, 'objects', 'mana-seed', 'village accessories 32x32.png');

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'empty', '/tilesets/mana-seed/village-accessories/village accessories 32x32.png', 0, 128, 32, 32, 1, 0
FROM asset WHERE name = 'Log Rack' AND pack_id = 'mana-seed';

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'low', '/tilesets/mana-seed/village-accessories/village accessories 32x32.png', 32, 128, 32, 32, 1, 0
FROM asset WHERE name = 'Log Rack' AND pack_id = 'mana-seed';

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'mid', '/tilesets/mana-seed/village-accessories/village accessories 32x32.png', 64, 128, 32, 32, 1, 0
FROM asset WHERE name = 'Log Rack' AND pack_id = 'mana-seed';

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'full', '/tilesets/mana-seed/village-accessories/village accessories 32x32.png', 96, 128, 32, 32, 1, 0
FROM asset WHERE name = 'Log Rack' AND pack_id = 'mana-seed';
