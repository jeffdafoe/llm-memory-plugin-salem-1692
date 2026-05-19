-- Rollback ZBBS-WORK-238. Drops v2 runtime state on object_refresh;
-- rows revert to v1's (object_id, attribute, amount) shape. Finite-
-- supply rows lose stock/regen config — engine treats them as
-- infinite on next load. Dwell config is destroyed (v1 had none).

BEGIN;

ALTER TABLE object_refresh DROP CONSTRAINT IF EXISTS object_refresh_dwell_period_positive;
ALTER TABLE object_refresh DROP CONSTRAINT IF EXISTS object_refresh_dwell_delta_negative;
ALTER TABLE object_refresh DROP CONSTRAINT IF EXISTS object_refresh_dwell_pair;
ALTER TABLE object_refresh DROP CONSTRAINT IF EXISTS object_refresh_supply_bounds;
ALTER TABLE object_refresh DROP CONSTRAINT IF EXISTS object_refresh_regen_only_when_finite;
ALTER TABLE object_refresh DROP CONSTRAINT IF EXISTS object_refresh_mode_only_when_finite;
ALTER TABLE object_refresh DROP CONSTRAINT IF EXISTS object_refresh_finite_regen;
ALTER TABLE object_refresh DROP CONSTRAINT IF EXISTS object_refresh_finite_pair;

ALTER TABLE object_refresh
    DROP COLUMN IF EXISTS dwell_period_minutes,
    DROP COLUMN IF EXISTS dwell_delta,
    DROP COLUMN IF EXISTS last_refresh_at,
    DROP COLUMN IF EXISTS refresh_period_hours,
    DROP COLUMN IF EXISTS refresh_mode,
    DROP COLUMN IF EXISTS available_quantity,
    DROP COLUMN IF EXISTS max_quantity;

DROP INDEX IF EXISTS idx_object_refresh_snapshot_gen;
ALTER TABLE object_refresh DROP COLUMN IF EXISTS snapshot_gen;
DROP SEQUENCE IF EXISTS object_refresh_snapshot_gen_seq;

COMMIT;
