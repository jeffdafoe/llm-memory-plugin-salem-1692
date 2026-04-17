-- ZBBS-026 down: remove tags, drop consolidated lantern assets.
-- Does NOT restore the old split Hanging Lantern (Dark)/(Lit) assets.

DELETE FROM asset_state_tag
WHERE state_id IN (
    SELECT s.id FROM asset_state s JOIN asset a ON a.id = s.asset_id
    WHERE a.name IN ('Hanging Lantern', 'Hanging Lantern (Mini)', 'Campfire')
);

DELETE FROM asset_state
WHERE asset_id IN (SELECT id FROM asset WHERE name IN ('Hanging Lantern', 'Hanging Lantern (Mini)'));

DELETE FROM asset
WHERE name IN ('Hanging Lantern', 'Hanging Lantern (Mini)');
