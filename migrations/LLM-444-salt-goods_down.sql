-- LLM-444 rollback: strip the salt boost off the three cooked dishes, remove the
-- cooks' buy-salt restock entries, the distributor's salt seed, and the salt good
-- itself.
--
-- CAVEAT: dev/pre-trade rollback, same posture as LLM-410/LLM-442's down. Once
-- sacks have been traded, pay_ledger / scene_quote reference the `salt` item_kind
-- name (ON UPDATE CASCADE, not ON DELETE), so the item_kind delete will fail — you
-- don't unwind a shipped economy. Reverses cleanly on the schema-only harness and
-- any DB with no salt trades yet. Each dish carried salt as its ONLY booster, so
-- resetting boost_inputs to [] restores the exact pre-444 recipe shape.

BEGIN;

-- 1. The three dishes back to their pre-444 shape: no boosters (inputs, rates,
--    and yields were never touched by the up migration, so nothing else to undo).
UPDATE item_recipe SET boost_inputs = '[]'::jsonb, updated_at = now()
 WHERE output_item IN ('stew', 'porridge', 'fried_meat');

-- 2. Remove the cooks' buy-salt entries (filter the restock array rather than
--    resetting it, so their produce/buy entries for everything else survive).
UPDATE actor_attribute
   SET params = jsonb_set(
       params,
       '{restock}',
       COALESCE(
           (SELECT jsonb_agg(e)
              FROM jsonb_array_elements(params->'restock') AS e
             WHERE NOT (e->>'item' = 'salt' AND e->>'source' = 'buy')),
           '[]'::jsonb
       )
   )
 WHERE actor_id IN ('019da6b2-7074-7b19-ab19-89b6fc3a29a1',   -- John Ellis (tavernkeeper)
                    '70419d0c-3668-428c-8bd8-633993c3aa60')   -- Hannah Boggs (innkeeper)
   AND slug IN ('tavernkeeper', 'innkeeper')
   AND jsonb_typeof(params->'restock') = 'array';

-- 3. Remove ONLY the seeded holding this migration owns (Josiah's bootstrap row)
--    — the LLM-410/LLM-442 scoped posture. Any OTHER salt holding is live economy
--    state this rollback must not destroy; if sacks have spread, the item_kind
--    delete below fails on its references, which is the correct outcome.
DELETE FROM actor_inventory
 WHERE actor_id = '019dcac2-e78a-715e-91b7-101f339b0891'
   AND item_kind = 'salt';

-- 4. Remove the price anchor, then the good.
DELETE FROM item_recipe WHERE output_item = 'salt';
DELETE FROM item_kind WHERE name = 'salt';

COMMIT;
