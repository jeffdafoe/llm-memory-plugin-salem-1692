-- ZBBS-053: Cap building footprint depth to ground extent.
--
-- ZBBS-050 derived footprint_h from sprite source height. For top-down 2D
-- with 2.5D-perspective buildings, the sprite includes a tall roof drawn
-- in perspective that doesn't physically block ground. The Blue House
-- at (2266, 662) ended up blocking 13 rows of pathfinding, cutting off
-- the corridor an NPC needed to walk past a lake east of the bridge.
--
-- New cap: depth ≈ width / 2 (top-down buildings are usually wider than
-- deep), floored at 3 so small structures stay meaningful, ceilinged at
-- the existing footprint_h so we never grow a footprint. Bridges and
-- other passages are excluded — they describe a walkable surface, not a
-- ground obstacle, and the full sprite extent matters there.
--
-- Visual rendering is unchanged. NPCs north of a building anchor still
-- y-sort behind the building's visible upper portion, which reads
-- correctly as "walking behind the house."

UPDATE asset
SET footprint_h = LEAST(footprint_h, GREATEST(footprint_w / 2, 3))
WHERE is_obstacle = TRUE
  AND category = 'structure';
