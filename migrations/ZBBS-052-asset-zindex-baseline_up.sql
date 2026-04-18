-- ZBBS-052: Shift the z_index baseline so 0 = ground.
--
-- ZBBS-051 set bridge.z_index = -1 to sort it below NPCs. That worked,
-- but it pushed the bridge below the terrain renderer (which was also at
-- z = 0) and made the bridge invisible. Cleaner mental model:
--
--   z = 0   terrain (the ground)
--   z = 1   ground overlays — bridges, future road decals, anything you
--           stand on top of
--   z = 10  characters and regular objects (OBJECT_Z in client code)
--   z = 11  attachments (relative to parent at z = 10)
--
-- All non-passage assets shift to z = 10 so they sort above bridges.
-- Bridge moves from -1 to 1.

ALTER TABLE asset ALTER COLUMN z_index SET DEFAULT 10;

UPDATE asset SET z_index = 10 WHERE is_passage = FALSE;
UPDATE asset SET z_index = 1 WHERE is_passage = TRUE;
