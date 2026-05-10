-- Down migration restores the legacy columns + constraint but cannot
-- recover per-actor values (we did not preserve them when migrating
-- to minute fields — the conversion is lossy on schedule_interval_hours).
-- Operators rolling back will need to re-seed schedule_interval_hours
-- if any rotation NPCs are restored.

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

COMMIT;
