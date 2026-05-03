-- ZBBS-117 down: drop village_concern table and content_generation column.

BEGIN;

ALTER TABLE village_object DROP COLUMN IF EXISTS content_generation;

DROP TABLE IF EXISTS village_concern;

DROP TYPE IF EXISTS concern_target_kind;
DROP TYPE IF EXISTS concern_source_kind;

COMMIT;
