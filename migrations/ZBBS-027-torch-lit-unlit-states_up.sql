-- ZBBS-027: Fix Torch state modeling.
--
-- Existing: single 'default' state at (0,0) with frame_count=5. But frame 0 is
-- the unlit sprite — the lit animation is only frames 1-4. So current torches
-- cycle through 5 frames that include an out-of-place unlit pose.
--
-- Fix: shrink 'default' to (16,0) frame_count=4 (the real lit animation) and
-- add an 'unlit' state at (0,0). Tag day/night following the Campfire pattern.

UPDATE asset_state
SET src_x = 16, frame_count = 4
WHERE state = 'default'
  AND sheet = '/tilesets/mana-seed/village-accessories/village anim 16x48.png'
  AND asset_id = (SELECT id FROM asset WHERE name = 'Torch' AND pack_id = 'mana-seed');

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'unlit', '/tilesets/mana-seed/village-accessories/village anim 16x48.png', 0, 0, 16, 48, 1, 0
FROM asset WHERE name = 'Torch' AND pack_id = 'mana-seed';

INSERT INTO asset_state_tag (state_id, tag)
SELECT s.id, 'night-active'
FROM asset_state s JOIN asset a ON a.id = s.asset_id
WHERE a.name = 'Torch' AND s.state = 'default';

INSERT INTO asset_state_tag (state_id, tag)
SELECT s.id, 'day-active'
FROM asset_state s JOIN asset a ON a.id = s.asset_id
WHERE a.name = 'Torch' AND s.state = 'unlit';
