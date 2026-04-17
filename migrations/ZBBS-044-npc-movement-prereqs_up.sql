-- ZBBS-044: Prerequisites for NPC pathfinding + walk-cycle animation.
--
-- Adds asset.is_obstacle so pathfinding can build a walkability grid (water
-- always impassable; placed objects impassable iff their asset is obstacle).
-- Seeds conservative defaults by category — refine per-asset later via UPDATE.
--
-- Also seeds walk + idle animation rows for the Woman A sprite so Martha
-- animates a walk cycle when she moves. Row layout (confirmed visually from
-- npc woman A v00.png):
--   Row 0 — south (facing camera), 4 walk frames
--   Row 1 — west, 4 walk frames
--   Row 2 — north (facing away), 4 walk frames
--   Row 3 — east, 4 walk frames
-- "Idle" uses frame 0 of the matching row — the neutral standing pose.

ALTER TABLE asset
    ADD COLUMN is_obstacle BOOLEAN NOT NULL DEFAULT FALSE;

-- Obstacle categories: blocks NPC walking, visually large or physically solid.
UPDATE asset SET is_obstacle = TRUE
    WHERE category IN ('tree', 'structure', 'fence', 'rock', 'stumps-and-logs', 'water-features');

-- Animation seed for the one seeded sprite (ZBBS-043).
INSERT INTO npc_sprite_animation (sprite_id, direction, animation, row_index, frame_count, frame_rate) VALUES
    ('22222222-3333-4444-5555-666666666666', 'south', 'walk', 0, 4, 6.0),
    ('22222222-3333-4444-5555-666666666666', 'west',  'walk', 1, 4, 6.0),
    ('22222222-3333-4444-5555-666666666666', 'north', 'walk', 2, 4, 6.0),
    ('22222222-3333-4444-5555-666666666666', 'east',  'walk', 3, 4, 6.0),
    ('22222222-3333-4444-5555-666666666666', 'south', 'idle', 0, 1, 1.0),
    ('22222222-3333-4444-5555-666666666666', 'west',  'idle', 1, 1, 1.0),
    ('22222222-3333-4444-5555-666666666666', 'north', 'idle', 2, 1, 1.0),
    ('22222222-3333-4444-5555-666666666666', 'east',  'idle', 3, 1, 1.0);
