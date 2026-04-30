BEGIN;
DROP INDEX IF EXISTS idx_village_object_xy;
DROP TABLE IF EXISTS object_refresh;

-- Restore the original ZBBS-073 source check (drop 'engine'). Any
-- existing rows with source='engine' would block this — there should
-- be none if down is run cleanly, but a manual cleanup may be needed.
ALTER TABLE agent_action_log DROP CONSTRAINT IF EXISTS agent_action_log_source_check;
ALTER TABLE agent_action_log ADD CONSTRAINT agent_action_log_source_check
    CHECK (source IN ('agent', 'magistrate', 'player'));

COMMIT;
