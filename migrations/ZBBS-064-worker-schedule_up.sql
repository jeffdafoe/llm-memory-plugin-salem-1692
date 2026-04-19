-- ZBBS-064: per-NPC scheduled behavior fields + 'worker' slug.
--
-- Four scheduling knobs land on npc. Each behavior decides which subset it
-- honors (see npc_scheduler.go for dispatch rules).
--
--   schedule_offset_hours: shifts a worker NPC's arrive/leave times off
--     the world's dawn/dusk boundaries. 0 = default (arrive dawn, leave
--     dusk). Positive = later (tavernkeeper ~10h opens as day winds down);
--     negative = earlier (baker ~-2h arrives before sunrise).
--     Ignored by non-worker behaviors. Range clamped to [-23, 23].
--
--   schedule_interval_hours + active_start_hour + active_end_hour:
--     per-NPC cadence for interval-driven behaviors (washerwoman,
--     town_crier). NPC fires at active_start_hour, then every
--     schedule_interval_hours, until past active_end_hour. Window wraps
--     midnight when start > end. All three must be set together
--     (enforced via schedule_all_or_none CHECK) — partial configuration
--     is a user error, not a supported mode. When all three are NULL,
--     the NPC falls back to the legacy world_rotation_time trigger.
--
--   last_shift_tick_at: the most recent boundary (arrive/leave for worker,
--     firing boundary for interval behaviors) the scheduler has already
--     dispatched. Matches the LastTransitionAt / LastRotationAt idiom in
--     world_phase — unchanged boundaries don't re-fire. Cleared when the
--     admin edits the schedule so a change takes effect next tick rather
--     than on the following boundary.

BEGIN;

ALTER TABLE npc
    ADD COLUMN schedule_offset_hours INTEGER NOT NULL DEFAULT 0
        CHECK (schedule_offset_hours BETWEEN -23 AND 23),
    ADD COLUMN schedule_interval_hours INTEGER
        CHECK (schedule_interval_hours IS NULL
               OR schedule_interval_hours BETWEEN 1 AND 24),
    ADD COLUMN active_start_hour INTEGER
        CHECK (active_start_hour IS NULL
               OR active_start_hour BETWEEN 0 AND 23),
    ADD COLUMN active_end_hour INTEGER
        CHECK (active_end_hour IS NULL
               OR active_end_hour BETWEEN 0 AND 23),
    ADD COLUMN last_shift_tick_at TIMESTAMPTZ,
    ADD CONSTRAINT schedule_all_or_none CHECK (
        (schedule_interval_hours IS NULL
            AND active_start_hour IS NULL
            AND active_end_hour IS NULL)
        OR
        (schedule_interval_hours IS NOT NULL
            AND active_start_hour IS NOT NULL
            AND active_end_hour IS NOT NULL)
    );

INSERT INTO npc_behavior (slug, display_name) VALUES
    ('worker', 'Worker');

COMMIT;
