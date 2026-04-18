-- ZBBS-058: remove Dandy sprites — they don't fit a Puritan village.
-- Seeded by ZBBS-057 but shouldn't have been. Cascades to animations.

BEGIN;

DELETE FROM npc_sprite
WHERE pack_id = 'mana-seed-npc-2'
  AND name LIKE 'Dandy %';

COMMIT;
