BEGIN;

-- Drop PC-only rows before reverting npc_id to NOT NULL.
DELETE FROM agent_action_log WHERE npc_id IS NULL;

ALTER TABLE agent_action_log
    ALTER COLUMN npc_id SET NOT NULL,
    DROP COLUMN IF EXISTS speaker_name;

COMMIT;
