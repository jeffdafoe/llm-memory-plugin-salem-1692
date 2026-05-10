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
--   3. Adds interval_hours=5 to the washerwoman / town_crier
--      attribute behaviors. Cadence now lives with the behavior
--      definition (attribute_definition.behaviors JSONB), not on
--      the actor row. Per-NPC overrides are gone by design.
--
-- Companion engine changes: the per-NPC rotation scheduler is rebuilt
-- to read cadence from attribute_definition.behaviors and the active
-- window from actor.schedule_start_minute / schedule_end_minute
-- (falling back to dawn/dusk when both are NULL). The
-- world_rotation_time-driven dispatch in world_rotation.go::applyRotation
-- is removed for these behaviors so they only fire from the per-NPC
-- scheduler (no double-firing).

BEGIN;

-- 1. Migrate any actor with legacy hour fields into the
--    minute-precision worker window. In current production this
--    catches Moses + Elizabeth, but the predicate is intentionally
--    broad so other environments / seeds that still carry rotation
--    NPCs (washerwoman / town_crier) also get their active windows
--    promoted. (The rotation cadence itself is now read from
--    attribute_definition.behaviors, so the conversion is lossless
--    for window data and the schedule_interval_hours value would
--    have been dropped anyway.) Skip rows that already have the
--    minute fields set so re-runs are idempotent.
UPDATE actor
   SET schedule_start_minute = active_start_hour * 60,
       schedule_end_minute   = active_end_hour   * 60
 WHERE schedule_start_minute IS NULL
   AND schedule_end_minute   IS NULL
   AND active_start_hour IS NOT NULL
   AND active_end_hour   IS NOT NULL;

-- 2. Drop the constraints that reference the legacy columns. Must
--    come before dropping the columns themselves so DROP COLUMN
--    doesn't depend on constraint cascade behavior.
ALTER TABLE actor DROP CONSTRAINT IF EXISTS actor_schedule_all_or_none;
ALTER TABLE actor DROP CONSTRAINT IF EXISTS actor_active_start_hour_check;
ALTER TABLE actor DROP CONSTRAINT IF EXISTS actor_active_end_hour_check;
ALTER TABLE actor DROP CONSTRAINT IF EXISTS actor_schedule_interval_hours_check;

-- 3. Drop the three legacy columns.
ALTER TABLE actor DROP COLUMN IF EXISTS schedule_interval_hours;
ALTER TABLE actor DROP COLUMN IF EXISTS active_start_hour;
ALTER TABLE actor DROP COLUMN IF EXISTS active_end_hour;

-- 4. Bake the rotation cadence into the attribute definition. Both
--    rotation-route attributes (washerwoman, town_crier) fire every
--    5 hours within their active window. Hardcoded for now; a future
--    refactor can promote interval_hours to a real column on
--    attribute_definition if more tuning surface is needed.
UPDATE attribute_definition
   SET behaviors = jsonb_set(behaviors, '{0,params,interval_hours}', '5'::jsonb, true)
 WHERE slug IN ('washerwoman', 'town_crier')
   AND behaviors->0->>'type' = 'rotation_route';

COMMIT;
