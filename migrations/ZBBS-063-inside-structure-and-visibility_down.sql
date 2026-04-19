BEGIN;

ALTER TABLE npc DROP COLUMN inside_structure_id;
ALTER TABLE asset DROP COLUMN visible_when_inside;

COMMIT;
