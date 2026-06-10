-- ZBBS-HOME-424 down: best-effort revert.
--
-- The up's home assignment is reverted only for the row it is known to have
-- touched in production data (Ezekiel Crane was the sole working-homeless
-- NPC at migration time); a generic "home = work → NULL" revert would also
-- strip keepers whose home was hand-set to their workplace. The deleted
-- vestigial service-item inventory rows are not restored — nothing in the
-- engine reads service kinds from inventory, so there is no state to put
-- back.

BEGIN;

UPDATE public.actor
   SET home_structure_id = NULL
 WHERE display_name = 'Ezekiel Crane'
   AND home_structure_id = work_structure_id;

COMMIT;
