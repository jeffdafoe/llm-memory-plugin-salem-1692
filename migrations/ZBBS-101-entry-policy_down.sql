-- ZBBS-101 rollback: restore asset.enterable, drop village_object.entry_policy.

BEGIN;

ALTER TABLE asset ADD COLUMN enterable BOOLEAN NOT NULL DEFAULT false;

-- Reconstruct asset.enterable from village_object.entry_policy: any asset
-- with at least one placed instance whose policy is not 'none' was
-- previously enterable.
UPDATE asset SET enterable = true
 WHERE id IN (
    SELECT DISTINCT asset_id
      FROM village_object
     WHERE entry_policy != 'none'
 );

ALTER TABLE village_object DROP COLUMN entry_policy;

DELETE FROM migrations_applied WHERE migration_name = 'ZBBS-101-entry-policy';

COMMIT;
