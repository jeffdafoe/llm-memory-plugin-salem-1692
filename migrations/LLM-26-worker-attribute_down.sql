-- LLM-26 rollback: remove the `worker` actor-attribute catalog row.
--
-- Manual-rollback-only (the deploy runner never applies _down). Removing the
-- definition does NOT strip the attribute from any actor that already carries it
-- (actor_attribute rows are independent and engine-checkpointed); it only retires
-- the slug from the catalog so the generic add-attribute path + editor dropdown no
-- longer offer it. sim.RemoveActorAttribute deliberately does not validate against
-- the catalog, so existing carriers can still be cleared after this runs.

BEGIN;

DELETE FROM public.attribute_definition WHERE slug = 'worker';

COMMIT;
