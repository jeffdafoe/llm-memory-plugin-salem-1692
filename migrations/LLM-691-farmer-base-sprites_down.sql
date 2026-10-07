-- LLM-691 down: Abraham Warren back on "Man B (v03)", the farmer sprite and
-- its pack row removed, and the rig / layers columns dropped.

BEGIN;

UPDATE public.actor
   SET sprite_id = 'fd124ed1-b225-466c-a3ee-455f7a25c59d'
 WHERE id = '019da6b5-3143-71e0-9f47-6bf3af456524'
   AND sprite_id = '691f0c01-0000-4000-8000-000000000001';

-- Any other actor still on a rig sprite would lose its look with the columns;
-- fail loudly rather than strand it on a sprite the client cannot draw.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM public.actor a JOIN public.npc_sprite s ON s.id = a.sprite_id WHERE s.rig IS NOT NULL) THEN
        RAISE EXCEPTION 'LLM-691 down: actors still use farmer_base sprites; move them first';
    END IF;
END $$;

DELETE FROM public.npc_sprite WHERE rig IS NOT NULL;
DELETE FROM public.tileset_pack WHERE id = 'mana-seed-farmer'
   AND NOT EXISTS (SELECT 1 FROM public.npc_sprite WHERE pack_id = 'mana-seed-farmer');

ALTER TABLE public.npc_sprite DROP CONSTRAINT IF EXISTS npc_sprite_layers_check;
ALTER TABLE public.npc_sprite DROP CONSTRAINT IF EXISTS npc_sprite_rig_check;
ALTER TABLE public.npc_sprite DROP COLUMN IF EXISTS layers;
ALTER TABLE public.npc_sprite DROP COLUMN IF EXISTS rig;

COMMIT;
