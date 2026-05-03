-- ZBBS-106: worker as implicit, derived from role attributes.
--
-- Until now, an NPC who works a shift had to carry both their role
-- attribute (tavernkeeper, blacksmith, herbalist, merchant) AND a
-- separate `worker` attribute. Two consequences:
--
--   1. Asymmetry with other scheduling. lamplighter / town_crier /
--      washerwoman describe their dispatch via attribute_definition.behaviors
--      JSONB ([{"type":"lamp_route"}], etc.); worker bypassed the JSONB
--      and was joined directly on slug='worker' in loadWorkerRows.
--   2. Manual data plumbing. Every new shift-bearing NPC needed both
--      attributes set. Forgetting `worker` silently dropped the NPC
--      from the home/work scheduler.
--
-- After this migration, the four shift-bearing roles carry
-- behaviors=[{"type":"worker"}]. loadWorkerRows is rewritten to join
-- attribute_definition.behaviors @> '[{"type":"worker"}]' and pull the
-- actor set from there — same code path the rotation/lamp behaviors
-- already use.
--
-- The standalone `worker` attribute_definition (and its actor_attribute
-- rows) are dropped — they're redundant once the role attributes carry
-- the worker hint themselves.
--
-- Note: a no-op `worker` entry is registered in attribute_dispatch.go's
-- behaviorHandlers map alongside this migration. The runtime worker
-- scheduler still drives the actual home/work walks; the JSONB entry
-- exists only as a discoverable marker for loadWorkerRows.

BEGIN;

-- Add worker behavior to the four shift-bearing role attributes.
UPDATE attribute_definition
   SET behaviors = '[{"type":"worker"}]'::jsonb
 WHERE slug IN ('tavernkeeper', 'blacksmith', 'herbalist', 'merchant')
   AND behaviors = '[]'::jsonb;

-- Drop existing actor_attribute assignments for the standalone worker
-- slug. The role attributes (which these actors also carry) now provide
-- the worker hint via behaviors JSONB.
DELETE FROM actor_attribute WHERE slug = 'worker';

-- Drop the standalone worker attribute_definition.
DELETE FROM attribute_definition WHERE slug = 'worker';

COMMIT;
