BEGIN;

DROP INDEX IF EXISTS idx_agent_action_log_huddle;
ALTER TABLE agent_action_log DROP COLUMN IF EXISTS huddle_id;

COMMIT;
