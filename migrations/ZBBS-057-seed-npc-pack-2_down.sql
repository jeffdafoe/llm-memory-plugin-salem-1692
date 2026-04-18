-- Rollback ZBBS-057: remove all Pack #2 sprites. Cascades to
-- npc_sprite_animation via the FK ON DELETE CASCADE.
BEGIN;

DELETE FROM npc_sprite WHERE pack_id = 'mana-seed-npc-2';
DELETE FROM tileset_pack WHERE id = 'mana-seed-npc-2';

COMMIT;
