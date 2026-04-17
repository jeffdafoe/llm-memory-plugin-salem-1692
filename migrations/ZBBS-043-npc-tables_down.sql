-- ZBBS-043 down

DROP TABLE IF EXISTS npc;
DROP TABLE IF EXISTS npc_sprite_animation;
DROP TABLE IF EXISTS npc_sprite;
DELETE FROM tileset_pack WHERE id = 'mana-seed-npc-1';
