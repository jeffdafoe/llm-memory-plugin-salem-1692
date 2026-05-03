-- ZBBS-105: messenger attribute_definition.
--
-- Adds the messenger attribute that the engine's summon flow uses to
-- find a runner when a summon is dispatched. The summoner walks to the
-- nearest summon_point village_object, the engine picks the nearest
-- messenger-tagged actor, and the messenger walks an errand chain
-- defined by the summon_errand table (added in ZBBS-106).
--
-- Messenger is mechanically driven — no LLM tick — so tools=[] and
-- instructions='' are intentional. Errand state lives in summon_errand
-- rather than in the behaviors JSONB, because behaviors describe
-- scheduled rotations (lamplighter, town_crier, washerwoman) and the
-- messenger reacts to dispatch instead.
--
-- Assignment is left to the editor: an admin creates a non-VA NPC and
-- attaches the messenger attribute via the new attributes chip UI.

BEGIN;

INSERT INTO attribute_definition (slug, display_name, description, tools, instructions) VALUES (
    'messenger',
    'Messenger',
    'Carries summons between villagers. When a summoner rings at a summon point, the nearest messenger walks to the summon point, then to the target''s location at dispatch time, delivers, and returns to where they started. Mechanical only — does not tick the LLM.',
    '[]'::jsonb,
    ''
);

COMMIT;
