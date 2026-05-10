-- ZBBS-WORK-218 down — drop the consolidation marker.

BEGIN;

DROP INDEX IF EXISTS idx_actor_relationship_consolidation;

ALTER TABLE actor_relationship
    DROP COLUMN IF EXISTS last_consolidated_at;

COMMIT;
