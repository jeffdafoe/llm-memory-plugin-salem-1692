-- ZBBS-072 down: drop the agent override column.

BEGIN;

ALTER TABLE npc
    DROP COLUMN agent_override_until;

COMMIT;
