-- ZBBS-108 down: drop the messenger_origin_structure_id column.

BEGIN;

ALTER TABLE summon_errand DROP COLUMN messenger_origin_structure_id;

COMMIT;
