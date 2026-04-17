-- ZBBS-035 down: drop all 4 laundry line assets and their states/tags.

DELETE FROM asset_state_tag
WHERE state_id IN (
    SELECT s.id FROM asset_state s JOIN asset a ON a.id = s.asset_id
    WHERE a.name LIKE 'Laundry (%' AND a.pack_id = 'mana-seed'
);

DELETE FROM asset_state
WHERE asset_id IN (SELECT id FROM asset WHERE name LIKE 'Laundry (%' AND pack_id = 'mana-seed');

DELETE FROM asset
WHERE name LIKE 'Laundry (%' AND pack_id = 'mana-seed';
