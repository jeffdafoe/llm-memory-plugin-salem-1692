-- ZBBS-177: coca tea — first tiredness-restoring item
--
-- Adds a stimulant item to the herbalist's inventory: coca tea, brewed
-- from the leaves of the coca plant. Restores +12 tiredness on consume —
-- the strongest single-dose item in the world (peer items: water +8
-- thirst, meat +10 hunger, stew +12 hunger but spread over 16 min).
--
-- Why a strong single dose: tiredness is a slow accrual (1/hr drain
-- baseline plus movement fatigue), so a small dose like +4 would force
-- multi-purchase chains that LLM NPCs typically don't carry through.
-- One cup of strong tea giving real pep matches the stimulant fiction
-- and makes the herbalist a meaningful recourse for exhaustion. No
-- dwell ticks because tea is "down it and go" — sit-and-sip semantics
-- are reserved for stew (16 min meal at the tavern).
--
-- Prudence Ward, the herbalist, gets seeded with 30 cups of stock to
-- match her berries supply. Lookup by display_name; idempotent on
-- (actor_id, item_kind) PK.
--
-- Idempotent throughout: NOT EXISTS guards on item_kind / item_satisfies
-- and on actor_inventory.

BEGIN;

INSERT INTO item_kind (name, display_label, category, sort_order, capabilities)
SELECT 'coca_tea', 'Coca Tea', 'drink', 40, ARRAY['portable']::text[]
 WHERE NOT EXISTS (SELECT 1 FROM item_kind WHERE name = 'coca_tea');

INSERT INTO item_satisfies (item_kind, attribute, amount)
SELECT 'coca_tea', 'tiredness', 12
 WHERE NOT EXISTS (
     SELECT 1 FROM item_satisfies
      WHERE item_kind = 'coca_tea' AND attribute = 'tiredness'
 );

INSERT INTO actor_inventory (actor_id, item_kind, quantity)
SELECT a.id, 'coca_tea'::varchar, 30::smallint
  FROM actor a
 WHERE a.display_name = 'Prudence Ward'
   AND NOT EXISTS (
       SELECT 1 FROM actor_inventory ai
        WHERE ai.actor_id = a.id AND ai.item_kind = 'coca_tea'
   );

COMMIT;
