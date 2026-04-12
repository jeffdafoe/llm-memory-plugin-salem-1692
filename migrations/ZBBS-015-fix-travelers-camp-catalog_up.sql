-- ZBBS-015: Fix travelers camp catalog
-- Corrects wrong sprite regions, names, missing animations, and adds slot data.

-- Fix campfire: lit state should be 5 frames starting at (0,0), not 4 frames at (32,0)
UPDATE asset_state SET src_x = 0, frame_count = 5
WHERE asset_id = 'campfire' AND state = 'default';

-- Fix campfire: unlit state is a 5-frame smoke animation at row 1 (y=32), not static at (0,0)
UPDATE asset_state SET src_x = 0, src_y = 32, frame_count = 5, frame_rate = 8
WHERE asset_id = 'campfire' AND state = 'unlit';

-- Add campfire slot for overlay objects (cooking pot, chicken)
INSERT INTO asset_slot (asset_id, slot_name, offset_x, offset_y)
VALUES ('campfire', 'top', 0, 0);

-- Rename "Camp Tripod" to "Cooking Pot" — it's a cooking pot on a tripod
UPDATE asset SET id = 'cooking-pot', name = 'Cooking Pot' WHERE id = 'camp-tripod';

-- Cooking pot is an overlay that fits the campfire "top" slot
UPDATE asset SET fits_slot = 'top' WHERE id = 'cooking-pot';

-- Rename "Cooking Spit" to "Chicken on Spit"
UPDATE asset SET id = 'chicken-spit', name = 'Chicken on Spit' WHERE id = 'cooking-spit';

-- Chicken on spit is also a campfire overlay
UPDATE asset SET fits_slot = 'top' WHERE id = 'chicken-spit';

-- Remove bogus "empty" state that pointed to blank cell (128,64)
DELETE FROM asset_state WHERE asset_id = 'chicken-spit' AND state = 'empty';

-- Fix bedroll states: "closed" and "open" (was "closed" and "default")
UPDATE asset SET default_state = 'closed' WHERE id = 'bedroll';
UPDATE asset_state SET state = 'open' WHERE asset_id = 'bedroll' AND state = 'default';
