BEGIN;

ALTER TABLE village_object
    DROP COLUMN IF EXISTS loiter_offset_x,
    DROP COLUMN IF EXISTS loiter_offset_y;

COMMIT;
