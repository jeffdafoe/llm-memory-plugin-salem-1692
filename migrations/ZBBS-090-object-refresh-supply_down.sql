BEGIN;

ALTER TABLE object_refresh DROP CONSTRAINT IF EXISTS object_refresh_period_positive;
ALTER TABLE object_refresh DROP CONSTRAINT IF EXISTS object_refresh_available_le_max;
ALTER TABLE object_refresh DROP CONSTRAINT IF EXISTS object_refresh_max_positive;
ALTER TABLE object_refresh DROP CONSTRAINT IF EXISTS object_refresh_quantity_nonneg;
ALTER TABLE object_refresh DROP CONSTRAINT IF EXISTS object_refresh_quantity_pair;
ALTER TABLE object_refresh DROP CONSTRAINT IF EXISTS object_refresh_mode_check;

ALTER TABLE object_refresh
    DROP COLUMN IF EXISTS last_refresh_at,
    DROP COLUMN IF EXISTS refresh_period_hours,
    DROP COLUMN IF EXISTS refresh_mode,
    DROP COLUMN IF EXISTS max_quantity,
    DROP COLUMN IF EXISTS available_quantity;

ALTER TABLE object_refresh DROP CONSTRAINT IF EXISTS object_refresh_attribute_fk;
ALTER TABLE object_refresh ADD CONSTRAINT object_refresh_attribute_check
    CHECK (attribute IN ('hunger','thirst','tiredness'));

DROP TABLE IF EXISTS refresh_attribute;

COMMIT;
