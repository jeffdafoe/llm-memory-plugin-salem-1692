-- LLM-253 rollback: remove the `forage_range` capability attribute entirely.
--
-- Delete ALL grants of the slug BEFORE the catalog row, not just Prudence's:
-- actor_attribute.slug references attribute_definition.slug ON DELETE RESTRICT, so
-- any other actor granted forage_range after this shipped (via the editor) would
-- block the catalog delete. A down migration fully reverses the up, so it strips
-- the capability globally (code_review). Like the up migration, the actor_attribute
-- delete touches checkpoint-written state — apply with the engine STOPPED
-- (deploy.sh's stop -> migrate -> start covers this) so a running binary doesn't
-- re-checkpoint a row it still holds in memory.

BEGIN;

DELETE FROM actor_attribute
 WHERE slug = 'forage_range';

DELETE FROM public.attribute_definition
 WHERE slug = 'forage_range';

COMMIT;
