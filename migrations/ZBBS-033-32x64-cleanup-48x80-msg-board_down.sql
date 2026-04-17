-- ZBBS-033 down: restore Outhouse name; other drops not reversible (would
-- re-guess anchors and fresh UUIDs).

UPDATE asset SET name = 'Outhouse'
WHERE name = 'Outhouse 1' AND pack_id = 'mana-seed';

DELETE FROM asset_state
WHERE asset_id IN (SELECT id FROM asset WHERE name = 'Outhouse 2' AND pack_id = 'mana-seed');
DELETE FROM asset
WHERE name = 'Outhouse 2' AND pack_id = 'mana-seed';
