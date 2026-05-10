-- Down migration restores the legacy columns + constraints. The
-- minute-fields → hour-fields conversion is best-effort:
-- schedule_interval_hours was always dropped (rotation cadence
-- lives on attribute_definition.behaviors post-251) and can't be
-- recovered. Operators rolling back AND restoring rotation actors
-- will need to re-seed schedule_interval_hours by hand.

BEGIN;

-- Strip interval_hours back out of the rotation behaviors. Operators
-- rolling back are expected to also revert the engine, which falls
-- back to the world_rotation_time path that doesn't read this field.
UPDATE attribute_definition
   SET behaviors = behaviors #- '{0,params,interval_hours}'
 WHERE slug IN ('washerwoman', 'town_crier')
   AND behaviors->0->>'type' = 'rotation_route';

ALTER TABLE actor ADD COLUMN IF NOT EXISTS schedule_interval_hours INTEGER;
ALTER TABLE actor ADD COLUMN IF NOT EXISTS active_start_hour INTEGER;
ALTER TABLE actor ADD COLUMN IF NOT EXISTS active_end_hour INTEGER;

ALTER TABLE actor
    ADD CONSTRAINT actor_schedule_all_or_none CHECK (
        (schedule_interval_hours IS NULL
         AND active_start_hour IS NULL
         AND active_end_hour IS NULL)
        OR
        (schedule_interval_hours IS NOT NULL
         AND active_start_hour IS NOT NULL
         AND active_end_hour IS NOT NULL)
    );

ALTER TABLE actor
    ADD CONSTRAINT actor_active_start_hour_check CHECK (
        active_start_hour IS NULL
        OR (active_start_hour >= 0 AND active_start_hour <= 23)
    );

ALTER TABLE actor
    ADD CONSTRAINT actor_active_end_hour_check CHECK (
        active_end_hour IS NULL
        OR (active_end_hour >= 0 AND active_end_hour <= 23)
    );

ALTER TABLE actor
    ADD CONSTRAINT actor_schedule_interval_hours_check CHECK (
        schedule_interval_hours IS NULL
        OR (schedule_interval_hours >= 1 AND schedule_interval_hours <= 24)
    );

-- Best-effort reverse conversion: restore hour fields for any actor
-- that now has a minute window but no legacy hour values. Integer
-- division truncates minute-precision values: an actor whose
-- schedule_start_minute = 510 (08:30) is restored as active_start_hour
-- = 8 (08:00) — sub-hour precision is lost. interval can't be
-- recovered (the up migration didn't preserve it) — defaults to 24
-- here so the row satisfies the all-or-none CHECK; operators
-- restoring rotation actors must re-seed interval explicitly.
UPDATE actor
   SET active_start_hour       = schedule_start_minute / 60,
       active_end_hour         = schedule_end_minute   / 60,
       schedule_interval_hours = 24
 WHERE schedule_start_minute IS NOT NULL
   AND schedule_end_minute   IS NOT NULL
   AND active_start_hour IS NULL
   AND active_end_hour   IS NULL;

COMMIT;
