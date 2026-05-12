-- ZBBS-HOME-275 down — revert the blacksmith skillet demand loop.
-- Restores the stew recipe and restock policies to pre-migration
-- shape, then removes the skillet item_kind. Most actor_inventory /
-- pay_ledger / scene_quote / errand-offer rows referencing skillet
-- are deleted because their FKs are ON UPDATE CASCADE only (no ON
-- DELETE), which would otherwise block the item_kind drop. The
-- transition-smoothing inventory bumps from step 7 of the up (John's
-- meat/milk/carrots/water/stew topped to 30) are NOT reverted —
-- they're harmless levels that match common steady-state values, and
-- accurately reverting would risk wiping legitimate post-deploy
-- gains. Best-effort revert; manual cleanup may be needed if the
-- migration has been in prod long enough to accumulate complex
-- references.

BEGIN;

-- Revert stew recipe to its pre-275 shape.
UPDATE item_recipe
   SET output_qty = 1,
       rate_qty = 5,
       rate_per_hours = 1,
       inputs = '[
         {"qty": 1, "item": "meat"},
         {"qty": 1, "item": "water"},
         {"qty": 1, "item": "milk"},
         {"qty": 1, "item": "carrots"}
       ]'::jsonb,
       updated_at = NOW()
 WHERE output_item = 'stew';

-- Restore John's tavernkeeper restock policy to its pre-275 shape.
UPDATE actor_attribute
   SET params = '{"restock": [
       {"max": 10, "item": "stew", "source": "produce"},
       {"max": 25, "item": "water", "source": "produce"},
       {"max": 20, "item": "ale", "source": "produce"},
       {"max": 15, "item": "bread", "source": "produce"},
       {"item": "cheese", "source": "buy", "target": 8},
       {"item": "meat", "source": "buy", "target": 5},
       {"item": "milk", "source": "buy", "target": 5},
       {"item": "carrots", "source": "buy", "target": 5}
   ]}'::jsonb
 WHERE actor_id = (SELECT id FROM actor WHERE display_name = 'John Ellis')
   AND slug = 'tavernkeeper';

-- Restore Ezekiel's blacksmith attribute params to empty.
UPDATE actor_attribute
   SET params = '{}'::jsonb
 WHERE actor_id = (SELECT id FROM actor WHERE display_name = 'Ezekiel Crane')
   AND slug = 'blacksmith';

-- Drop dependents whose FK to item_kind isn't ON DELETE CASCADE.
-- (The ones that ARE cascade-on-delete — actor_buy_state,
-- actor_produce_state, item_recipe, item_satisfies — clean up
-- automatically when item_kind row goes.) Best-effort revert: if the
-- migration has been in prod long enough to accumulate complex
-- references, manual cleanup may be needed.
DELETE FROM scene_quote WHERE item_kind = 'skillet';
DELETE FROM pay_ledger WHERE item_kind = 'skillet';
DELETE FROM npc_errand_offer WHERE fetch_item_kind = 'skillet';
DELETE FROM actor_restock_in_progress WHERE item_kind = 'skillet';
DELETE FROM actor_delivery_in_progress WHERE item_kind = 'skillet';
DELETE FROM actor_inventory WHERE item_kind = 'skillet';
-- gatherable_node FK has no ON DELETE either. Skillet should never
-- appear here (it's a tool, not a foraged kind), but a stray
-- admin/test row would block the item_kind drop. Defensive.
DELETE FROM gatherable_node WHERE item_kind = 'skillet';

-- Now drop the item_kind row itself.
DELETE FROM item_kind WHERE name = 'skillet';

COMMIT;
