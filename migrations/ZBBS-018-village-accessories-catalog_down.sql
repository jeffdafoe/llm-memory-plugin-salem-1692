-- ZBBS-018 rollback

-- Remove fits_slot assignments
UPDATE asset SET fits_slot = NULL WHERE name LIKE 'Shop Sign%';
UPDATE asset SET fits_slot = NULL WHERE name LIKE 'Banner%';

-- Remove slot definitions
DELETE FROM asset_slot WHERE asset_id IN (
    SELECT id FROM asset WHERE name IN ('Notice Board', 'Signpost (Stone)', 'Signpost (Wood)', 'Hanging Sign', 'Hanging Banner')
);

-- Remove new assets
DELETE FROM asset_state WHERE asset_id IN (
    SELECT id FROM asset WHERE name IN ('Notice Board', 'Signpost (Stone)', 'Signpost (Wood)', 'Cart', 'Torch', 'Outhouse', 'Large Crate')
    AND created_at > NOW() - INTERVAL '1 day'
);
DELETE FROM asset WHERE name IN ('Notice Board', 'Signpost (Stone)', 'Signpost (Wood)', 'Cart', 'Torch', 'Outhouse', 'Large Crate')
AND created_at > NOW() - INTERVAL '1 day';
