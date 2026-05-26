-- ZBBS-WORK-328 down: drop the gatherable metadata column.
-- The backfilled gather_item values are dropped with the column.

BEGIN;

ALTER TABLE object_refresh
    DROP COLUMN gather_item;

COMMIT;
