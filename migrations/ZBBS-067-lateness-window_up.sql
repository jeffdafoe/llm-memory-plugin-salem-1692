-- ZBBS-067: per-NPC lateness window.
--
-- Jeff wants the village to feel less clockwork. Adding a per-NPC
-- lateness_window_minutes fuzzes scheduled behavior firing times
-- within a window AFTER the nominal boundary.
--
-- Model:
--   * Scope: worker (arrive + leave) and rotation behaviors
--     (washerwoman + town_crier). Lamplighter excluded for now — it's
--     triggered inline with world-phase transitions, not via the NPC
--     scheduler; adding lateness there is a separate change.
--   * Semantics: actual_fire = nominal_boundary + deterministic_offset,
--     where deterministic_offset = hash(npc_id, boundary) mod window.
--     Deterministic so the offset is stable across ticks and restarts;
--     re-rolling per tick would mean the NPC keeps missing the window.
--     Asymmetric (always late, never early) so the DB last_shift_tick_at
--     stamp still monotonically trails the nominal boundary.
--   * 0 = current deterministic behavior. Capped at 180 (3h) —
--     higher values would make "scheduled" NPCs indistinguishable
--     from unscheduled ones.

BEGIN;

ALTER TABLE npc
    ADD COLUMN lateness_window_minutes INTEGER NOT NULL DEFAULT 0
        CHECK (lateness_window_minutes BETWEEN 0 AND 180);

COMMIT;
