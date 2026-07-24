-- Revert LLM-514's data activation: detach + de-register the `constable`
-- attribute and return the Red Monastery (Meeting House) to
-- visible_when_inside=true. Manual-rollback only (the deploy runner never applies
-- _down). Leaves the LLM-514 engine code inert (no constable carrier) and the
-- Meeting House back in its prior visible-interior mode; pair with a code revert
-- to fully back out.
--
-- Order matters: actor_attribute.slug -> attribute_definition.slug is
-- ON DELETE RESTRICT, so every carrier row must be removed before the catalog row.

BEGIN;

DELETE FROM actor_attribute WHERE slug = 'constable';

UPDATE asset
SET visible_when_inside = true
WHERE id = '389b2ebe-9430-4691-9b85-3e64898f19cb';

DELETE FROM public.attribute_definition WHERE slug = 'constable';

COMMIT;
