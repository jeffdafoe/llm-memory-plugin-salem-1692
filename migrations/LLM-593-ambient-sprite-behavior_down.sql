-- LLM-593 down: drop the ambient slug, leaving the movement slug in place.
--
-- Removes only "ambient" rather than resetting behaviors to ["waterfowl"],
-- so a sprite that gained other slugs after the up-migration keeps them.

BEGIN;

UPDATE public.npc_sprite
   SET behaviors = (
           SELECT COALESCE(jsonb_agg(slug), '[]'::jsonb)
             FROM jsonb_array_elements(behaviors) AS slug
            WHERE slug <> '"ambient"'::jsonb
       )
 WHERE behaviors @> '["ambient"]'::jsonb;

COMMIT;
