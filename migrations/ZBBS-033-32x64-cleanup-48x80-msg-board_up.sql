-- ZBBS-033: Clean up village accessories 32x64.png (mostly misidentified) and
-- drop Message Board from 48x80.png.
--
-- Visual review revealed:
-- * (0,0) "Outhouse" is correct — rename to "Outhouse 1" (preserves 2 placements)
-- * (32,0) "Posted Notice" was misidentified — it's actually another outhouse variant.
--   Replace with new "Outhouse 2" asset.
-- * Everything else on the 32x64 sheet was misidentified as gates/crates. Drop.
-- * 48x80 (192,0) "Message Board" — drop per user request.

-- ----- 32x64 sheet cleanup -----

-- Rename existing Outhouse -> Outhouse 1 (preserves 2 placements).
UPDATE asset SET name = 'Outhouse 1'
WHERE name = 'Outhouse' AND pack_id = 'mana-seed';

-- Drop Posted Notice (0 placements, misidentified).
DELETE FROM asset_state WHERE asset_id IN (SELECT id FROM asset WHERE name = 'Posted Notice');
DELETE FROM asset WHERE name = 'Posted Notice';

-- Drop Large Gate, Arched Gate, Large Crate (all 0 placements, misidentified).
DELETE FROM asset_state
WHERE asset_id IN (SELECT id FROM asset WHERE name IN ('Large Gate', 'Arched Gate', 'Large Crate'));
DELETE FROM asset
WHERE name IN ('Large Gate', 'Arched Gate', 'Large Crate');

-- Create Outhouse 2 at (32, 0) with same shape/anchor as Outhouse 1.
INSERT INTO asset (id, name, category, default_state, anchor_x, anchor_y, layer, pack_id, source_file) VALUES
    (gen_random_uuid(), 'Outhouse 2', 'structure', 'default', 0.5, 0.85, 'objects', 'mana-seed', 'village accessories 32x64.png');

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'default', '/tilesets/mana-seed/village-accessories/village accessories 32x64.png', 32, 0, 32, 64, 1, 0
FROM asset WHERE name = 'Outhouse 2' AND pack_id = 'mana-seed';

-- ----- 48x80 sheet cleanup -----

-- Drop Message Board at (192, 0), 0 placements.
DELETE FROM asset_state
WHERE asset_id IN (SELECT id FROM asset WHERE name = 'Message Board');
DELETE FROM asset WHERE name = 'Message Board';
