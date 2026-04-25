BEGIN;

ALTER TABLE npc
    DROP COLUMN last_agent_tick_at;

COMMIT;
