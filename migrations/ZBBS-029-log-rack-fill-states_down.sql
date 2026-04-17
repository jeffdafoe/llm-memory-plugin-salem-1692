-- ZBBS-029 down: drop the consolidated Log Rack asset.
-- Does NOT restore the 4 split assets.

DELETE FROM asset_state
WHERE asset_id IN (SELECT id FROM asset WHERE name = 'Log Rack' AND pack_id = 'mana-seed');

DELETE FROM asset
WHERE name = 'Log Rack' AND pack_id = 'mana-seed';
