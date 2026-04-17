-- ZBBS-045 down: swap back.

UPDATE npc_sprite_animation SET row_index = 99 WHERE direction = 'east' AND row_index = 1;
UPDATE npc_sprite_animation SET row_index = 1 WHERE direction = 'west' AND row_index = 3;
UPDATE npc_sprite_animation SET row_index = 3 WHERE direction = 'east' AND row_index = 99;
