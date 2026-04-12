-- ZBBS-015 rollback

-- Restore bedroll states
UPDATE asset_state SET state = 'default' WHERE asset_id = 'bedroll' AND state = 'open';
UPDATE asset SET default_state = 'default' WHERE id = 'bedroll';

-- Restore cooking spit empty state
INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
VALUES ('cooking-spit', 'empty', '/tilesets/mana-seed/travelers-camp/travelers camp 32x32.png', 128, 64, 32, 32, 1, 0);

-- Rename chicken-spit back to cooking-spit
UPDATE asset SET fits_slot = NULL WHERE id = 'chicken-spit';
UPDATE asset SET id = 'cooking-spit', name = 'Cooking Spit' WHERE id = 'chicken-spit';

-- Rename cooking-pot back to camp-tripod
UPDATE asset SET fits_slot = NULL WHERE id = 'cooking-pot';
UPDATE asset SET id = 'camp-tripod', name = 'Camp Tripod' WHERE id = 'cooking-pot';

-- Remove campfire slot
DELETE FROM asset_slot WHERE asset_id = 'campfire' AND slot_name = 'top';

-- Restore campfire unlit to static at (0,0)
UPDATE asset_state SET src_x = 0, src_y = 0, frame_count = 1, frame_rate = 0
WHERE asset_id = 'campfire' AND state = 'unlit';

-- Restore campfire lit to 4 frames at (32,0)
UPDATE asset_state SET src_x = 32, frame_count = 4
WHERE asset_id = 'campfire' AND state = 'default';
