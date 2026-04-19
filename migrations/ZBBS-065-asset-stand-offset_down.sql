BEGIN;

ALTER TABLE asset
    DROP COLUMN stand_offset_y,
    DROP COLUMN stand_offset_x;

COMMIT;
