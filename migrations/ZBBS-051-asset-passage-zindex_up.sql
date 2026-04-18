-- ZBBS-051: Walkable passages + per-asset render z-index.
--
-- ZBBS-050 swept bridges into is_obstacle = TRUE along with all other
-- structures, which is wrong — bridges are how you cross water, not what
-- you walk around. Two new fields fix it cleanly:
--
--   is_passage : the asset's footprint becomes walkable, even over water.
--                Pathfinder applies water → obstacles → passages so passages
--                always win.
--   z_index    : asset render priority. With y_sort_enabled on Objects,
--                Godot uses z_index as the primary sort key, so a bridge
--                with z_index = -1 always draws below NPCs (z_index = 0)
--                regardless of where the NPC's feet are along the y axis.
--                Generally useful for ground-level overlays (rugs, mats,
--                future road decals) — anything you stand on top of.
--
-- Bridge gets is_passage = TRUE, is_obstacle = FALSE, z_index = -1.

ALTER TABLE asset
    ADD COLUMN is_passage BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN z_index INT NOT NULL DEFAULT 0;

UPDATE asset
SET is_passage = TRUE,
    is_obstacle = FALSE,
    z_index = -1
WHERE name = 'Bridge';
