-- LLM-514: activate the constable "walk the rounds" behaviour + fix the Meeting
-- House interior rendering. Three DATA changes on the live prod DB:
--
--   (0) Register `constable` in the attribute_definition catalog. actor_attribute.
--       slug is FK'd to attribute_definition(slug), so the slug must be defined
--       before it can be attached. The engine keys behaviour on attribute PRESENCE
--       only (Actor.Attributes["constable"]) and loads just slug + display_name at
--       boot -- it never reads tools/instructions/behaviours -- so a minimal row
--       suffices; this also makes `constable` mintable via the generic attribute
--       tools + the editor dropdown (mirrors LLM-26's `worker` registration).
--
--   (1) Grant Gideon Marsh (the live-created constable NPC, actor
--       4561da54-eb08-46c8-8f05-ddc0aadaebff) the `constable` attribute. The
--       LLM-514 engine code gates the interval-driven business-circuit route on
--       this marker, so the feature is inert until the row exists.
--
--   (2) Flip the "Red Monastery" asset (389b2ebe-9430-4691-9b85-3e64898f19cb --
--       the Meeting House) to visible_when_inside=false. It was decorative scenery
--       left in the "visible interior, no stand offset" mode, so an NPC attributed
--       inside rendered on the raw door tile (looked like he stood outside) and the
--       PC's structure_enter landed on top of him. Every real building (the Tavern,
--       the houses) is visible_when_inside=false -- inside occupants are hidden and
--       the building reads "occupied".
--
-- attribute_definition is reference data (loaded once at boot, not checkpointed);
-- actor_attribute is engine-checkpointed; asset is boot-loaded reference data.
-- Applied engine-down by the deploy (down->migrate->up), so no checkpoint clobber.
-- Guarded / rerun-safe for the schema-only fresh DB the test harness + CI replay:
-- the catalog insert is ON CONFLICT DO NOTHING; the attribute insert fires only
-- when Gideon exists and isn't already tagged; the asset UPDATE matches 0 rows
-- harmlessly.
--
-- NOT reseeded here: Gideon + the Meeting House structure/room themselves. They
-- were created live (live-first) and live durably in prod; a schema-only fresh DB
-- has no sprite/asset/village_object prerequisites, so seeding them would either
-- FK-fail (breaking CI) or no-op. Durable fresh-DB reproduction is a re-baseline
-- concern, tracked in active-work.

BEGIN;

INSERT INTO public.attribute_definition (slug, display_name, description, scope)
VALUES (
    'constable',
    'Constable',
    'Keeps the peace and walks the rounds of the village businesses on a periodic circuit (LLM-514). A watch-keeper role; the rounds behaviour is engine-driven off this marker attribute.',
    'actor'
)
ON CONFLICT (slug) DO NOTHING;

INSERT INTO actor_attribute (actor_id, slug)
SELECT '4561da54-eb08-46c8-8f05-ddc0aadaebff', 'constable'
WHERE EXISTS (
    SELECT 1 FROM actor WHERE id = '4561da54-eb08-46c8-8f05-ddc0aadaebff'
) AND NOT EXISTS (
    SELECT 1 FROM actor_attribute
    WHERE actor_id = '4561da54-eb08-46c8-8f05-ddc0aadaebff' AND slug = 'constable'
);

UPDATE asset
SET visible_when_inside = false
WHERE id = '389b2ebe-9430-4691-9b85-3e64898f19cb'
  AND visible_when_inside = true;

COMMIT;
