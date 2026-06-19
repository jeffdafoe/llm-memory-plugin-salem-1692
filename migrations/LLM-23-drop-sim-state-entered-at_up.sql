-- LLM-23: drop the dead actor.sim_state_entered_at column.
--
-- The column was frozen at the v2-launch seed (added DEFAULT now() in
-- WORK-243) and nothing has updated it since, so a row read "asleep for
-- three weeks" nonsense that sent debugging down dead ends. Nothing in
-- engine/sim reads it; the live State enum plus last_agent_tick_at /
-- sleeping_until carry the real signal. Removed, not maintained.
--
-- The actor table is checkpointed by the running engine, so this migration
-- must be applied with the engine STOPPED (stop -> migrate -> start): the
-- old binary's checkpoint INSERT still names this column and would error
-- against the dropped column mid-flight. Do NOT run via the unconditional
-- deploy.sh restart path.

BEGIN;

ALTER TABLE actor DROP COLUMN IF EXISTS sim_state_entered_at;

COMMIT;
