-- ZBBS-025 down: remove Notice Board + tags, drop state_tag table
-- Does NOT restore the ZBBS-024 flat layout — run ZBBS-024 up again if you need that.

DELETE FROM asset_state
WHERE asset_id IN (SELECT id FROM asset WHERE name = 'Notice Board' AND pack_id = 'mana-seed');

DELETE FROM asset
WHERE name = 'Notice Board' AND pack_id = 'mana-seed';

DROP TABLE IF EXISTS asset_state_tag;
