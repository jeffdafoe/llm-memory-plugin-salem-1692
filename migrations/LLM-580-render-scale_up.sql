-- LLM-580: per-sprite render scale. The client had a hardcoded 2x for every
-- NPC sprite — sized for the 32x32 human sheets, which made a duck (most of
-- its cell) stand hip-height to a villager. render_scale is a render hint the
-- client applies to the character sprite, its anchor offset, and the ground
-- decal; the engine never reads it. Data-driven so the next animal (chickens
-- are drawn smaller still) is a number in a row, not a client special case.

BEGIN;

ALTER TABLE public.npc_sprite
    ADD COLUMN IF NOT EXISTS render_scale double precision NOT NULL DEFAULT 2.0;

UPDATE public.npc_sprite
   SET render_scale = 1.0
 WHERE pack_id = 'mana-seed-livestock';

COMMIT;
