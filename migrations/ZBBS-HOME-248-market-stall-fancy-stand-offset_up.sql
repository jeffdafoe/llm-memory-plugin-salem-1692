-- ZBBS-HOME-248 — Market Stall (Fancy) was never configured the way
-- the Tiled and Wood stall variants were: missing stand_offset_x/y
-- and visible_when_inside still at the default `false`. The result
-- was that a keeper assigned to a "Fancy" stall had their sprite
-- hidden on arrival inside (visible_when_inside=false) and the
-- editor's orange stand marker did not render (NULL stand_offset).
--
-- Observed on Ellis Farm (the only "Fancy" stall in the village):
-- Elizabeth Ellis was logically inside the structure but invisible
-- in the world, and the asset had no stand block in the editor.
--
-- The values match the Tiled variant — same sheet, same anchor,
-- same gameplay role.

BEGIN;

UPDATE asset
   SET visible_when_inside = true,
       stand_offset_x = -1,
       stand_offset_y = -1
 WHERE name = 'Market Stall (Fancy)';

COMMIT;
