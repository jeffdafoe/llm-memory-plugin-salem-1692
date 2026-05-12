-- ZBBS-HOME-267 — settings dial for the PC presence-cleanup sweep.
--
-- The engine's per-tick sweep (engine/pc_presence_sweep.go) clears
-- inside_structure_id + current_huddle_id for PCs whose
-- last_pc_input_at is older than this many minutes. Without the sweep,
-- a PC who disconnects (browser close, network drop, crash) stays
-- "inside" their last structure forever — NPCs in that scene keep
-- perceiving the ghost on every reactor tick and burn LLM spend
-- greeting an absent customer (observed 2026-05-12 with Jefferey
-- ghost-stuck in the Inn, 9h of Hannah Boggs blank-context loop).
--
-- Distinct from pc_idle_sleep_minutes (default 15): auto-bed is a
-- lodger-only state-machine entry that keeps the PC "asleep in their
-- room". Presence-clear is more aggressive (5m default) because it
-- covers non-lodger PCs whose only correct treatment is "you are no
-- longer in this scene" — there's no lodging machinery to fall back
-- to. Setting it to 0 disables the sweep entirely (operator escape
-- hatch); a value below 1 is treated as 0.

BEGIN;

INSERT INTO setting (key, value, description)
VALUES
    ('pc_presence_clear_minutes', '5',
     'Minutes of PC input silence before the engine clears their inside_structure_id and current_huddle_id, stopping co-located NPCs from perceiving a ghost. Set to 0 to disable.')
ON CONFLICT (key) DO NOTHING;

COMMIT;
