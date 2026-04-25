-- ZBBS-072: agent override column on NPCs (M6.1).
--
-- Adds agent_override_until: a timestamp through which any LLM-driven
-- "agent" action owns the NPC. The worker, rotation, and social
-- schedulers each short-circuit when this is in the future, leaving the
-- NPC under agent control until the timestamp expires.
--
-- Set by the (future) /agent/tick endpoint when an action with a known
-- duration is dispatched (e.g. a walk that will take ~12 minutes). Cleared
-- naturally by time passing — no row update needed when the override
-- expires; the next tick simply sees now() past the stored value.
--
-- Pairs with a forward-stamp of last_shift_tick_at done by the same
-- endpoint, so the scheduler doesn't snap the NPC back to a missed
-- boundary the moment override expires (the missed boundary is treated
-- as "the agent did something else with that hour, drop it").
--
-- M6.1 is the schema + scheduler short-circuit only. Audit log
-- (agent_action_log) lands with M6.2 when the endpoint exists to write
-- to it.

BEGIN;

ALTER TABLE npc
    ADD COLUMN agent_override_until TIMESTAMPTZ;

COMMIT;
