-- LLM-582: the ducks were shrunk too far. LLM-580 introduced npc_sprite.
-- render_scale and set the livestock pack to 1.0 against the villagers' 2.0,
-- correcting a duck that stood hip-height to a villager at the old hardcoded
-- 2x. It overshot: at 1.0 a duck reads as roughly ankle height and is easy to
-- lose against the terrain entirely — the reported symptom was not being able
-- to find the ducks at all.
--
-- 1.4 splits the difference, landing the duck around knee height. Purely a
-- client draw hint (the engine never reads render_scale), so re-tuning later
-- is this same one-row UPDATE. npc_sprite is a boot-loaded catalog the engine
-- never writes back, so the new value takes effect at the deploy restart and
-- cannot be clobbered by a checkpoint.
--
-- Scoped to the duck rows rather than the pack: pack_id
-- 'mana-seed-livestock' currently holds only the 8 duck sprites, but the next
-- animal added to it (chickens are drawn smaller still) should not silently
-- inherit a duck's scale.

BEGIN;

UPDATE public.npc_sprite
   SET render_scale = 1.4
 WHERE pack_id = 'mana-seed-livestock'
   AND name LIKE 'Duck (%';

COMMIT;
