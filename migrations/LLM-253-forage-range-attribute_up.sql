-- LLM-253: ranged forage cue — the `forage_range` capability attribute.
--
-- The sage trade loop is frozen because Prudence Ward (herbalist) is never made
-- aware of the UNOWNED Sage Bush ~80 tiles from her routine: the owned-bush cue
-- (buildForage) is owner-only, and the at-bush cue (findGatherableCue) is
-- proximity-bound, so an unowned-and-distant source falls between them. This adds
-- a "ranged forage" capability: a tagged forager perceives the nearest ripe
-- UNOWNED forage-to-sell source for each low forage-restock item at ANY distance,
-- with a move_to handle. The engine keys on the attribute's PRESENCE only
-- (perception reads ActorSnapshot.AttributeSlugs); params is unused, like `worker`.
--
-- This is a deliberate, role-scoped departure from the LLM-76/77/79 earned-memory
-- no-omniscience posture — the "herbalist gift", justified as domain knowledge (an
-- herbalist knows where herbs grow).

BEGIN;

-- Part 1: register `forage_range` in the attribute catalog. attribute_definition
-- is pure reference data — NOT engine-checkpointed (loaded once at boot via
-- LoadAll, no checkpoint write-back), so this rides a normal deploy with no
-- stop-first requirement. The row makes the slug mintable through the generic
-- attribute-assignment paths (sim.AddActorAttribute / the editor's add-attribute
-- dropdown, which list scope IN ('actor','both')) and makes those paths validate
-- it instead of rejecting it as ErrUnknownAttribute. The in-memory
-- AttributeDefinition loads only slug + display_name; the description/scope columns
-- are catalog metadata. ON CONFLICT keeps a re-run a clean no-op (slug is the PK).
INSERT INTO public.attribute_definition (slug, display_name, description, scope)
VALUES (
    'forage_range',
    'Forager (ranged)',
    'Knows where herbs grow wild: perceives UNOWNED forage-to-sell sources for their low forage-restock items at any distance, with a move_to handle — the "herbalist gift" ranged forage cue (LLM-253).',
    'actor'
)
ON CONFLICT (slug) DO NOTHING;

-- Part 2: grant `forage_range` to Prudence Ward. actor_attribute IS
-- CHECKPOINT-WRITTEN by the engine (raw params written back at SaveSnapshot, with
-- a snapshot_gen stale-row sweep). Apply with the engine STOPPED (stop -> migrate
-- -> start), or a running binary's next checkpoint could sweep a row it never
-- loaded; deploy.sh already does stop -> migrate -> start, so a normal deploy is
-- safe. snapshot_gen defaults to 0 — the post-migration boot loads this row into
-- Prudence's Actor.Attributes (loadAll selects every row regardless of gen) and
-- re-checkpoints it at the live gen, so it survives the next stale-sweep. params
-- '{}' because the attribute is presence-only. ON CONFLICT no-op keeps a re-run
-- clean.
-- Guard the grant on Prudence EXISTING: actor_attribute.actor_id has an FK to
-- actor(id), and on a fresh schema-only DB (the migration-replay test harness, or
-- a brand-new deploy that hasn't seeded actors) her actor row isn't present — an
-- unconditional INSERT would violate the FK. INSERT ... SELECT ... WHERE EXISTS
-- inserts 0 rows there and the real row once she's seeded. (LLM-59 sidestepped this
-- with UPDATE; a brand-new attribute row needs INSERT, so we gate it explicitly.)
INSERT INTO actor_attribute (actor_id, slug, params)
SELECT '019dbcec-1149-7149-8a49-2cdb54680b86', 'forage_range', '{}'::jsonb
WHERE EXISTS (SELECT 1 FROM actor WHERE id = '019dbcec-1149-7149-8a49-2cdb54680b86')
ON CONFLICT (actor_id, slug) DO NOTHING;

-- Validate the seeded state, failing loud rather than silently shipping the feature
-- disabled. A schema-only DB has an EMPTY actor table — nothing to grant, skip. A
-- seeded DB (any actors present) MUST have Prudence, and her grant MUST have applied
-- — so a stale/changed actor id (the grant silently no-ops) is caught here at deploy
-- rather than leaving the cue permanently off (code_review).
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM actor) THEN
        IF NOT EXISTS (SELECT 1 FROM actor WHERE id = '019dbcec-1149-7149-8a49-2cdb54680b86') THEN
            RAISE EXCEPTION 'LLM-253: seeded actor table exists but Prudence actor id 019dbcec-1149-7149-8a49-2cdb54680b86 is missing (stale id?)';
        END IF;
        IF NOT EXISTS (
            SELECT 1 FROM actor_attribute
            WHERE actor_id = '019dbcec-1149-7149-8a49-2cdb54680b86'
              AND slug = 'forage_range'
        ) THEN
            RAISE EXCEPTION 'LLM-253: Prudence exists but her forage_range actor_attribute was not applied';
        END IF;
    END IF;
END $$;

COMMIT;
