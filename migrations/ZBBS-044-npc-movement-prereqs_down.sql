-- ZBBS-044 down

DELETE FROM npc_sprite_animation
    WHERE sprite_id = '22222222-3333-4444-5555-666666666666';

ALTER TABLE asset DROP COLUMN IF EXISTS is_obstacle;
