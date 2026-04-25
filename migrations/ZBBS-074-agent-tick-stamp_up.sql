-- ZBBS-074: per-NPC stamp for agent-tick idempotency (M6.3).
--
-- The agent dispatcher runs on the unified server tick (60s wall-clock).
-- Game-time = wall-clock in Salem, so an NPC's "tick the agent at the
-- top of every game-hour" means at most once per wall-clock hour.
-- last_agent_tick_at records the in-world hour boundary the dispatcher
-- already fired for, so subsequent server ticks within that hour skip
-- the NPC. Same idempotency model the worker scheduler uses with
-- last_shift_tick_at.
--
-- NULL means the NPC has never been ticked. The dispatcher fires on
-- the first eligible boundary (skipping the catch-up of past hours).

BEGIN;

ALTER TABLE npc
    ADD COLUMN last_agent_tick_at TIMESTAMPTZ;

COMMIT;
