-- ZBBS-WORK-219 down — drop the per-actor consolidation marker.

BEGIN;

DROP INDEX IF EXISTS idx_actor_narrative_state_consolidation;

ALTER TABLE actor_narrative_state
    DROP COLUMN IF EXISTS last_consolidated_at;

COMMIT;
