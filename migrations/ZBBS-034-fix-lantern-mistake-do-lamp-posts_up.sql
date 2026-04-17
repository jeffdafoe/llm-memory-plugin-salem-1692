-- ZBBS-034: Fix ZBBS-028 mistake and apply intended work on the 48x80 sheet.
--
-- ZBBS-028 misread the original instructions and touched the 16x16 sheet when
-- the target was the 48x80 sheet (tall Lamp Posts, not small standalone Lanterns).
-- The 6 deleted "Shop Sign" rows were guessed names with 0 placements; not
-- restored. This migration:
--   1. Reverts Lantern state changes on the 16x16 sheet (back to single 'default').
--   2. Drops Lantern 2 / Lantern 3 (wrong sheet entirely).
--   3. Adds unlit state to existing Lamp Post on 48x80 at (192, 80); renames its
--      'default' state to 'lit' and migrates the 1 existing placement so the
--      sprite doesn't change visually.
--   4. Creates Lamp Post 2 (unlit 0,80 + lit 48,80) and Lamp Post 3 (unlit 96,80
--      + lit 144,80).
--   5. Tags all lamp post unlit=day-active, lit=night-active.

-- ========== Part A: Revert ZBBS-028 Lantern mistakes on 16x16 sheet ==========

-- Drop day/night tags for Lantern/Lantern 2/Lantern 3 states (cascade via FK).
DELETE FROM asset_state_tag
WHERE state_id IN (
    SELECT s.id FROM asset_state s JOIN asset a ON a.id = s.asset_id
    WHERE a.name IN ('Lantern', 'Lantern 2', 'Lantern 3') AND a.pack_id = 'mana-seed'
);

-- Drop Lantern 2 and Lantern 3 entirely (wrong sheet; 0 placements).
DELETE FROM asset_state
WHERE asset_id IN (SELECT id FROM asset WHERE name IN ('Lantern 2', 'Lantern 3') AND pack_id = 'mana-seed');
DELETE FROM asset WHERE name IN ('Lantern 2', 'Lantern 3') AND pack_id = 'mana-seed';

-- Revert Lantern to single 'default' state at (144, 32).
DELETE FROM asset_state
WHERE state = 'unlit'
  AND asset_id = (SELECT id FROM asset WHERE name = 'Lantern' AND pack_id = 'mana-seed');

UPDATE asset_state SET state = 'default'
WHERE state = 'lit'
  AND asset_id = (SELECT id FROM asset WHERE name = 'Lantern' AND pack_id = 'mana-seed');

UPDATE asset SET default_state = 'default'
WHERE name = 'Lantern' AND pack_id = 'mana-seed';

-- ========== Part B: Apply intended work to Lamp Post on 48x80 sheet ==========

-- Rename existing 'default' state to 'lit'. Migrate the 1 placement so the sprite
-- doesn't change (the asset_state row is the same, just the state name changes).
UPDATE village_object SET current_state = 'lit'
WHERE current_state = 'default'
  AND asset_id = (SELECT id FROM asset WHERE name = 'Lamp Post' AND pack_id = 'mana-seed');

UPDATE asset_state SET state = 'lit'
WHERE state = 'default'
  AND asset_id = (SELECT id FROM asset WHERE name = 'Lamp Post' AND pack_id = 'mana-seed');

UPDATE asset SET default_state = 'unlit'
WHERE name = 'Lamp Post' AND pack_id = 'mana-seed';

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'unlit', '/tilesets/mana-seed/village-accessories/village accessories 48x80.png', 192, 80, 48, 80, 1, 0
FROM asset WHERE name = 'Lamp Post' AND pack_id = 'mana-seed';

-- Lamp Post 2
INSERT INTO asset (id, name, category, default_state, anchor_x, anchor_y, layer, pack_id, source_file) VALUES
    (gen_random_uuid(), 'Lamp Post 2', 'prop', 'unlit', 0.5, 0.85, 'objects', 'mana-seed', 'village accessories 48x80.png');

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'unlit', '/tilesets/mana-seed/village-accessories/village accessories 48x80.png', 0, 80, 48, 80, 1, 0
FROM asset WHERE name = 'Lamp Post 2' AND pack_id = 'mana-seed';

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'lit', '/tilesets/mana-seed/village-accessories/village accessories 48x80.png', 48, 80, 48, 80, 1, 0
FROM asset WHERE name = 'Lamp Post 2' AND pack_id = 'mana-seed';

-- Lamp Post 3
INSERT INTO asset (id, name, category, default_state, anchor_x, anchor_y, layer, pack_id, source_file) VALUES
    (gen_random_uuid(), 'Lamp Post 3', 'prop', 'unlit', 0.5, 0.85, 'objects', 'mana-seed', 'village accessories 48x80.png');

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'unlit', '/tilesets/mana-seed/village-accessories/village accessories 48x80.png', 96, 80, 48, 80, 1, 0
FROM asset WHERE name = 'Lamp Post 3' AND pack_id = 'mana-seed';

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'lit', '/tilesets/mana-seed/village-accessories/village accessories 48x80.png', 144, 80, 48, 80, 1, 0
FROM asset WHERE name = 'Lamp Post 3' AND pack_id = 'mana-seed';

-- Tags for all three lamp post assets.
INSERT INTO asset_state_tag (state_id, tag)
SELECT s.id, 'day-active'
FROM asset_state s JOIN asset a ON a.id = s.asset_id
WHERE a.name IN ('Lamp Post', 'Lamp Post 2', 'Lamp Post 3')
  AND a.pack_id = 'mana-seed'
  AND s.state = 'unlit';

INSERT INTO asset_state_tag (state_id, tag)
SELECT s.id, 'night-active'
FROM asset_state s JOIN asset a ON a.id = s.asset_id
WHERE a.name IN ('Lamp Post', 'Lamp Post 2', 'Lamp Post 3')
  AND a.pack_id = 'mana-seed'
  AND s.state = 'lit';
