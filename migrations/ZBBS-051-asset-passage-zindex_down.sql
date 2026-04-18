-- ZBBS-051 down. Restores the prior obstacle flag on bridges so the
-- behavior matches ZBBS-050 if rolled back.

UPDATE asset SET is_obstacle = TRUE WHERE name = 'Bridge';

ALTER TABLE asset
    DROP COLUMN is_passage,
    DROP COLUMN z_index;
