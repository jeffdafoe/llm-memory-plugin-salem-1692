-- ZBBS-027 down: revert Torch to single 5-frame state, remove unlit + tags.

DELETE FROM asset_state_tag
WHERE state_id IN (
    SELECT s.id FROM asset_state s JOIN asset a ON a.id = s.asset_id
    WHERE a.name = 'Torch'
);

DELETE FROM asset_state
WHERE state = 'unlit'
  AND asset_id = (SELECT id FROM asset WHERE name = 'Torch' AND pack_id = 'mana-seed');

UPDATE asset_state
SET src_x = 0, frame_count = 5
WHERE state = 'default'
  AND sheet = '/tilesets/mana-seed/village-accessories/village anim 16x48.png'
  AND asset_id = (SELECT id FROM asset WHERE name = 'Torch' AND pack_id = 'mana-seed');
