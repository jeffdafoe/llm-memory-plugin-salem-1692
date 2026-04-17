-- ZBBS-028: Bring in 2 more Lantern variants from the 16x16 sheet;
-- fix the existing Lantern to have proper unlit state.
--
-- Cells affected on village accessories 16x16.png:
-- * (64, 0) = row 0 col 4 — currently "Shop Sign (Star)" (misidentified). DELETE.
-- * (0, 16) = row 1 col 0 — currently "Shop Sign (Paw)" (misidentified). Becomes Lantern 2 unlit.
-- * (16, 16) = row 1 col 1 — currently "Shop Sign (Coffee)" (misidentified). Becomes Lantern 2 lit.
-- * (32, 16) = row 1 col 2 — currently "Shop Sign (Mushroom)" (misidentified). Becomes Lantern 3 unlit.
-- * (48, 16) = row 1 col 3 — currently "Shop Sign (Hat)" (misidentified). Becomes Lantern 3 lit.
-- * (64, 16) = row 1 col 4 — currently "Shop Sign (Crossed Swords)" (misidentified). Becomes Lantern unlit state.
--
-- All 6 target rows have 0 placements; safe to delete and repurpose.

-- ----- Remove misidentified shop sign assets -----
DELETE FROM asset_state
WHERE asset_id IN (SELECT id FROM asset WHERE name IN (
    'Shop Sign (Star)', 'Shop Sign (Paw)', 'Shop Sign (Coffee)',
    'Shop Sign (Mushroom)', 'Shop Sign (Hat)', 'Shop Sign (Crossed Swords)'
));

DELETE FROM asset
WHERE name IN (
    'Shop Sign (Star)', 'Shop Sign (Paw)', 'Shop Sign (Coffee)',
    'Shop Sign (Mushroom)', 'Shop Sign (Hat)', 'Shop Sign (Crossed Swords)'
);

-- ----- Existing Lantern: rename 'default' state to 'lit', add 'unlit', make unlit the default -----
UPDATE asset_state SET state = 'lit'
WHERE state = 'default'
  AND asset_id = (SELECT id FROM asset WHERE name = 'Lantern' AND pack_id = 'mana-seed');

UPDATE asset SET default_state = 'unlit'
WHERE name = 'Lantern' AND pack_id = 'mana-seed';

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'unlit', '/tilesets/mana-seed/village-accessories/village accessories 16x16.png', 64, 16, 16, 16, 1, 0
FROM asset WHERE name = 'Lantern' AND pack_id = 'mana-seed';

-- ----- Lantern 2 -----
INSERT INTO asset (id, name, category, default_state, anchor_x, anchor_y, layer, pack_id, source_file) VALUES
    (gen_random_uuid(), 'Lantern 2', 'prop', 'unlit', 0.5, 0.85, 'objects', 'mana-seed', 'village accessories 16x16.png');

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'unlit', '/tilesets/mana-seed/village-accessories/village accessories 16x16.png', 0, 16, 16, 16, 1, 0
FROM asset WHERE name = 'Lantern 2' AND pack_id = 'mana-seed';

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'lit', '/tilesets/mana-seed/village-accessories/village accessories 16x16.png', 16, 16, 16, 16, 1, 0
FROM asset WHERE name = 'Lantern 2' AND pack_id = 'mana-seed';

-- ----- Lantern 3 -----
INSERT INTO asset (id, name, category, default_state, anchor_x, anchor_y, layer, pack_id, source_file) VALUES
    (gen_random_uuid(), 'Lantern 3', 'prop', 'unlit', 0.5, 0.85, 'objects', 'mana-seed', 'village accessories 16x16.png');

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'unlit', '/tilesets/mana-seed/village-accessories/village accessories 16x16.png', 32, 16, 16, 16, 1, 0
FROM asset WHERE name = 'Lantern 3' AND pack_id = 'mana-seed';

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'lit', '/tilesets/mana-seed/village-accessories/village accessories 16x16.png', 48, 16, 16, 16, 1, 0
FROM asset WHERE name = 'Lantern 3' AND pack_id = 'mana-seed';

-- ----- Tags -----
INSERT INTO asset_state_tag (state_id, tag)
SELECT s.id, 'day-active'
FROM asset_state s JOIN asset a ON a.id = s.asset_id
WHERE a.name IN ('Lantern', 'Lantern 2', 'Lantern 3')
  AND a.pack_id = 'mana-seed'
  AND s.state = 'unlit';

INSERT INTO asset_state_tag (state_id, tag)
SELECT s.id, 'night-active'
FROM asset_state s JOIN asset a ON a.id = s.asset_id
WHERE a.name IN ('Lantern', 'Lantern 2', 'Lantern 3')
  AND a.pack_id = 'mana-seed'
  AND s.state = 'lit';
