-- ZBBS-028 down: remove Lantern 2 + Lantern 3 and lantern tags, revert Lantern to 'default' state.
-- Does NOT restore the deleted shop sign rows.

DELETE FROM asset_state_tag
WHERE state_id IN (
    SELECT s.id FROM asset_state s JOIN asset a ON a.id = s.asset_id
    WHERE a.name IN ('Lantern', 'Lantern 2', 'Lantern 3') AND a.pack_id = 'mana-seed'
);

DELETE FROM asset_state
WHERE asset_id IN (SELECT id FROM asset WHERE name IN ('Lantern 2', 'Lantern 3') AND pack_id = 'mana-seed');

DELETE FROM asset
WHERE name IN ('Lantern 2', 'Lantern 3') AND pack_id = 'mana-seed';

DELETE FROM asset_state
WHERE state = 'unlit'
  AND asset_id = (SELECT id FROM asset WHERE name = 'Lantern' AND pack_id = 'mana-seed');

UPDATE asset_state SET state = 'default'
WHERE state = 'lit'
  AND asset_id = (SELECT id FROM asset WHERE name = 'Lantern' AND pack_id = 'mana-seed');

UPDATE asset SET default_state = 'default'
WHERE name = 'Lantern' AND pack_id = 'mana-seed';
