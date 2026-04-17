-- ZBBS-045: Fix east/west rows on the Woman A sprite.
--
-- ZBBS-044 seeded the animations with east=row 3 and west=row 1. Visual
-- inspection of the sheet shows the opposite: row 1 depicts the character
-- facing the viewer's right (east) and row 3 facing the viewer's left
-- (west). The swap made the character walk with body oriented opposite to
-- direction of travel — "walking backwards." This migration swaps the
-- row_index on the two directions.
--
-- Temporary sentinel value (99) keeps the PK constraint from complaining
-- during the swap.

UPDATE npc_sprite_animation SET row_index = 99 WHERE direction = 'east' AND row_index = 3;
UPDATE npc_sprite_animation SET row_index = 3 WHERE direction = 'west' AND row_index = 1;
UPDATE npc_sprite_animation SET row_index = 1 WHERE direction = 'east' AND row_index = 99;
