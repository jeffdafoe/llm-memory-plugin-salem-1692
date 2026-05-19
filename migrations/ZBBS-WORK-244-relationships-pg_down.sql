-- ZBBS-WORK-244 down: reverse all changes from _up in inverse order.
-- Does NOT drop last_consolidated_at — that column predates this slice
-- (it's in the baseline), so this migration never added it.

BEGIN;

-- B. Drop snapshot-gen substrate — indexes + columns first, sequences
-- last (dependency-safe teardown).
DROP INDEX IF EXISTS idx_npc_acquaintance_snapshot_gen;
DROP INDEX IF EXISTS idx_actor_narrative_state_snapshot_gen;
DROP INDEX IF EXISTS idx_actor_relationship_snapshot_gen;

ALTER TABLE npc_acquaintance      DROP COLUMN IF EXISTS snapshot_gen;
ALTER TABLE actor_narrative_state DROP COLUMN IF EXISTS snapshot_gen;
ALTER TABLE actor_relationship    DROP COLUMN IF EXISTS snapshot_gen;

DROP SEQUENCE IF EXISTS npc_acquaintance_snapshot_gen_seq;
DROP SEQUENCE IF EXISTS actor_narrative_state_snapshot_gen_seq;
DROP SEQUENCE IF EXISTS actor_relationship_snapshot_gen_seq;

-- A. Drop v2 telemetry column.
ALTER TABLE actor_relationship DROP COLUMN IF EXISTS dropped_fact_count;

COMMIT;
