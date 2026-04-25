-- ZBBS-073 down: drop the agent action log table.

BEGIN;

DROP TABLE IF EXISTS agent_action_log;

COMMIT;
