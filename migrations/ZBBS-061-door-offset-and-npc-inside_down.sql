BEGIN;

ALTER TABLE asset
    DROP COLUMN door_offset_x,
    DROP COLUMN door_offset_y;

ALTER TABLE npc
    DROP COLUMN inside;

COMMIT;
