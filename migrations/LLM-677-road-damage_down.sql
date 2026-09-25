-- LLM-677 down: put the two fallen-tree assets back as they were — Fallen Log
-- not an obstacle, Fallen Tree 2 an obstacle, both on a single-tile footprint.
--
-- REFUSES while a road obstacle is placed. A placed obstacle means a road is
-- blocked right now, and the rolled-back engine does not know road damage: the
-- log would stay in the road with nobody offered the clearing. Clear it first —
-- POST /api/village/umbilical/object/damage {id, action: "repair"} removes it —
-- then re-run.

BEGIN;

DO $$
DECLARE placed int;
BEGIN
    SELECT count(*) INTO placed
      FROM village_object
     WHERE 'road_obstacle' = ANY(tags);
    IF placed > 0 THEN
        RAISE EXCEPTION
            'LLM-677 down: % road obstacle(s) still in the village. Clear them first (engine running, umbilical), then re-run.',
            placed;
    END IF;
END $$;

UPDATE asset
   SET is_obstacle = false, footprint_left = 0, footprint_right = 0, footprint_top = 0, footprint_bottom = 0
 WHERE id = '1d0accaa-dc6b-4ab1-8d3d-ad3076b5efd5';

UPDATE asset
   SET is_obstacle = true, footprint_left = 0, footprint_right = 0, footprint_top = 0, footprint_bottom = 0
 WHERE id = '6e894192-421d-45a0-b67c-d34d8f0f5885';

COMMIT;
