-- ZBBS-109 down: drop the target_dispatch_structure_id column.

BEGIN;

ALTER TABLE summon_errand DROP COLUMN target_dispatch_structure_id;

COMMIT;
