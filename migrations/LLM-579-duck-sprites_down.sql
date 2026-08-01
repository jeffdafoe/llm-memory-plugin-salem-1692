-- LLM-579 down: remove the duck sprites, the livestock pack, the 'swim'
-- animation vocabulary, and the behaviors column.
--
-- The sprite deletes fail on the actor.sprite_id FK if a live actor still
-- uses a duck sprite — deliberate: reassign or remove those actors first
-- rather than silently orphaning their render.

BEGIN;

DELETE FROM public.npc_sprite_animation
WHERE sprite_id IN (SELECT id FROM public.npc_sprite WHERE pack_id = 'mana-seed-livestock');

DELETE FROM public.npc_sprite WHERE pack_id = 'mana-seed-livestock';

DELETE FROM public.tileset_pack WHERE id = 'mana-seed-livestock';

ALTER TABLE public.npc_sprite_animation
    DROP CONSTRAINT npc_sprite_animation_animation_check;
ALTER TABLE public.npc_sprite_animation
    ADD CONSTRAINT npc_sprite_animation_animation_check
    CHECK (((animation)::text = ANY ((ARRAY[
        'idle'::character varying,
        'walk'::character varying])::text[])));

ALTER TABLE public.npc_sprite
    DROP COLUMN IF EXISTS behaviors;

COMMIT;
