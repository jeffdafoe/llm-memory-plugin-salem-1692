-- LLM-442 rollback: restore the inputless 4/hr nail recipe, remove the smith's
-- buy-iron restock entry, the distributor's iron seed, and the iron good itself.
--
-- CAVEAT: dev/pre-trade rollback, same posture as LLM-410's down. Once bars have
-- been traded, pay_ledger / scene_quote reference the `iron` item_kind name (ON
-- UPDATE CASCADE, not ON DELETE), so the item_kind delete will fail — you don't
-- unwind a shipped economy. Reverses cleanly on the schema-only harness and any
-- DB with no iron trades yet.

BEGIN;

-- 1. Nail recipe back to its pre-442 shape: 4/hr, no boosters.
UPDATE item_recipe
   SET rate_qty     = 4,
       boost_inputs = '[]'::jsonb,
       updated_at   = now()
 WHERE output_item = 'nail';

-- 2. Remove Ezekiel's buy-iron entry (filter the restock array rather than
--    resetting it, so his produce/forage entries survive untouched).
UPDATE actor_attribute
   SET params = jsonb_set(
       params,
       '{restock}',
       COALESCE(
           (SELECT jsonb_agg(e)
              FROM jsonb_array_elements(params->'restock') AS e
             WHERE NOT (e->>'item' = 'iron' AND e->>'source' = 'buy')),
           '[]'::jsonb
       )
   )
 WHERE actor_id = '019da6f9-1b4c-7dda-bb6b-3248cdafb2c4'
   AND slug = 'blacksmith'
   AND jsonb_typeof(params->'restock') = 'array';

-- 3. Remove ONLY the seeded holding this migration owns (Josiah's bootstrap
--    row) — the LLM-410 scoped posture. Any OTHER iron holding is live economy
--    state this rollback must not destroy; if bars have spread, the item_kind
--    delete below fails on its references, which is the correct outcome (you
--    don't unwind a shipped economy).
DELETE FROM actor_inventory
 WHERE actor_id = '019dcac2-e78a-715e-91b7-101f339b0891'
   AND item_kind = 'iron';

-- 4. Remove the price anchor, then the good.
DELETE FROM item_recipe WHERE output_item = 'iron';
DELETE FROM item_kind WHERE name = 'iron';

COMMIT;
