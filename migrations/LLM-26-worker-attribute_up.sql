-- LLM-26: register the `worker` actor attribute in the catalog.
--
-- solicit_work (the worker-initiated service-for-pay primitive, LLM-26 / PR #604)
-- is gated to actors carrying the `worker` attribute. The engine keys on the
-- attribute's PRESENCE only (Actor.Attributes["worker"]); it never reads the
-- definition's tools/instructions/behaviors columns (the in-memory
-- AttributeDefinition loads only slug + display_name). But the GENERIC
-- attribute-assignment paths validate against this catalog:
--   * sim.AddActorAttribute (POST /api/village/admin/npc/attribute/add) returns
--     ErrUnknownAttribute for an unregistered slug;
--   * the editor's "add attribute" dropdown (GET /api/village/npc-behaviors)
--     lists only rows with scope IN ('actor','both').
-- This row makes `worker` mintable through those existing generic tools rather
-- than a raw actor_attribute INSERT.
--
-- Pure reference-data seed. attribute_definition is NOT engine-checkpointed
-- (loaded once at startup via LoadAll, no checkpoint write-back), so this rides a
-- normal deploy with no stop-first requirement; the restarted engine picks up the
-- new slug. ON CONFLICT keeps a re-run a clean no-op (slug is the PK).

BEGIN;

INSERT INTO public.attribute_definition (slug, display_name, description, scope)
VALUES (
    'worker',
    'Worker',
    'Takes service-for-pay jobs: can offer their labor to a co-present villager for coins via solicit_work (LLM-26).',
    'actor'
)
ON CONFLICT (slug) DO NOTHING;

COMMIT;
