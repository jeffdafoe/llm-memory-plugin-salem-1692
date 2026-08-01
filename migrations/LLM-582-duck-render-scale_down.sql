-- Revert the duck render scale to the LLM-580 value.

BEGIN;

UPDATE public.npc_sprite
   SET render_scale = 1.0
 WHERE pack_id = 'mana-seed-livestock'
   AND name LIKE 'Duck (%';

COMMIT;
