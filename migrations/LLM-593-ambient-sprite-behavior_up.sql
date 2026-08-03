-- LLM-593: tag the duck sprites as ambient scenery.
--
-- npc_sprite.behaviors is the authoring surface for engine behavior: placing
-- an actor with the sprite is the whole flow, no per-actor attribute grant.
-- The slugs compose, and the two on a duck answer different questions:
--
--   waterfowl -- HOW it moves (swim the connected water region, come ashore)
--   ambient   -- WHAT it counts as (scenery; nothing it does is recorded)
--
-- Splitting them is what makes the next animal free. A chicken gets
-- ["ambient"] plus whatever drives it, and it stays out of the action log,
-- the admin Village tab, and the atmosphere roster and digest with no code
-- change. Before this, the record-keeping gates would have had to learn each
-- new species by name.
--
-- Deliberately NOT applied by pack_id: 'mana-seed-livestock' holds only the 8
-- ducks today, but a future non-ambient sprite could land in the same pack —
-- and the failure mode of a wrongly-ambient actor is silent (it simply stops
-- appearing in the record), which is the kind that goes unnoticed. Matching
-- the waterfowl behavior instead ties the grant to what the sprite already
-- provably is.
--
-- jsonb_path_query_array over the union keeps this idempotent: re-running
-- cannot produce ["ambient","ambient"], and the WHERE clause skips rows that
-- already carry it.
--
-- npc_sprite is a boot-loaded catalog the engine never writes back, so this
-- takes effect at the deploy restart and no checkpoint can clobber it.

BEGIN;

UPDATE public.npc_sprite
   SET behaviors = jsonb_path_query_array(behaviors || '["ambient"]'::jsonb, '$[*]')
 WHERE behaviors @> '["waterfowl"]'::jsonb
   AND NOT behaviors @> '["ambient"]'::jsonb;

COMMIT;
