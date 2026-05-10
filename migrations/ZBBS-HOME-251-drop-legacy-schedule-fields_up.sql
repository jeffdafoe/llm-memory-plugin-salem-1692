-- ZBBS-HOME-251 — Drop the legacy hour-based per-NPC schedule fields.
--
-- The actor table has long carried two parallel scheduling systems:
--
--   * Hour-based (legacy): schedule_interval_hours + active_start_hour
--     + active_end_hour. Drove a per-NPC ROTATION pattern (fires every N
--     hours within a window) intended for washerwoman / town_crier
--     style behaviors. Held by a CHECK constraint that required all
--     three or none.
--
--   * Minute-precision (ZBBS-071): schedule_start_minute +
--     schedule_end_minute. Drives the WORKER shift pattern (walk
--     home → work at start, work → home at end).
--
-- Two configured actors were stranded on the hour-based fields
-- (Moses James and Elizabeth Ellis, both market-stall keepers).
-- They're keepers, not rotation NPCs, so the rotation pattern was
-- the wrong model anyway — they should be on minute-precision
-- worker shifts. Until then, the npc_scheduler treated them as
-- rotators that fire at 6:00 AM but never get walked home at end
-- of shift; observed at 19:21 EDT today with both keepers still
-- at their stalls past their 19:00 active_end_hour.
--
-- This migration:
--   1. Promotes Moses + Elizabeth to minute-precision worker shifts
--      (start=360 / end=1140 = 6:00 AM – 7:00 PM).
--   2. Drops the legacy columns + the schedule_all_or_none CHECK
--      constraint.
--
-- Companion engine + client changes remove all code that read the
-- legacy fields and the rotation-via-per-NPC-schedule scheduler
-- branch. The world_rotation_time-driven washerwoman / town_crier
-- dispatch (in world_rotation.go::applyRotation) keeps working —
-- it lives off attribute slugs, not the schedule fields — but no
-- actor currently has those behaviors so it's dormant in practice.

BEGIN;

-- 1. Migrate the two hour-based actors. Convert hours-of-day to
--    minutes-of-day. We pick the schedule_interval_hours value as
--    informational — it's about to be dropped — and only need the
--    start/end conversion. Skip rows that already have minute
--    fields set (idempotent re-run safety).
UPDATE actor
   SET schedule_start_minute = active_start_hour * 60,
       schedule_end_minute   = active_end_hour   * 60
 WHERE schedule_start_minute IS NULL
   AND schedule_end_minute   IS NULL
   AND active_start_hour IS NOT NULL
   AND active_end_hour   IS NOT NULL;

-- 2. Drop the all-or-none CHECK on the legacy trio. Must come
--    before dropping the columns themselves.
ALTER TABLE actor DROP CONSTRAINT IF EXISTS actor_schedule_all_or_none;

-- 3. Drop the three legacy columns.
ALTER TABLE actor DROP COLUMN IF EXISTS schedule_interval_hours;
ALTER TABLE actor DROP COLUMN IF EXISTS active_start_hour;
ALTER TABLE actor DROP COLUMN IF EXISTS active_end_hour;

COMMIT;
