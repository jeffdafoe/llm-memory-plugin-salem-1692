-- ZBBS-106 down: restore the standalone worker attribute_definition and
-- re-attach it to every NPC carrying a shift-bearing role attribute.

BEGIN;

-- Recreate the worker attribute_definition.
INSERT INTO attribute_definition (slug, display_name, description, scope, tools, instructions, behaviors)
VALUES (
    'worker',
    'Worker',
    'Shift-bearing villager who walks between home and work each day. Hours come from schedule_start_minute/schedule_end_minute on the actor (or fall back to global dawn/dusk).',
    'actor',
    '[]'::jsonb,
    '',
    '[]'::jsonb
);

-- Strip the worker behaviors hint from the role attributes.
UPDATE attribute_definition
   SET behaviors = '[]'::jsonb
 WHERE slug IN ('tavernkeeper', 'blacksmith', 'herbalist', 'merchant')
   AND behaviors = '[{"type":"worker"}]'::jsonb;

-- Re-attach the worker attribute to every actor that holds a
-- shift-bearing role. ON CONFLICT defends against re-running the down
-- migration against partially-rolled-back state.
INSERT INTO actor_attribute (actor_id, slug)
SELECT DISTINCT aa.actor_id, 'worker'
  FROM actor_attribute aa
 WHERE aa.slug IN ('tavernkeeper', 'blacksmith', 'herbalist', 'merchant')
ON CONFLICT (actor_id, slug) DO NOTHING;

COMMIT;
