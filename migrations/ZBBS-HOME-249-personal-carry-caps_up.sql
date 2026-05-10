-- ZBBS-HOME-249 — Personal carry caps + smaller batches.
--
-- Two intentional changes:
--
-- 1. Shrink stew batch size. Previously a stew "execution" produced
--    10 bowls every 2 hours, which together with the produce-tick
--    headroom rounding (executions = headroom / output_qty) meant
--    stew could only refill from current=0 — sell ANY stew below
--    the cap and the headroom was < 10 and no batch could fire.
--    output_qty=1 + rate_qty=5 keeps roughly the same throughput
--    (~5/h) but as continuous one-bowl ticks. Same shape as ale,
--    bread, milk, etc. Headroom math now works for any partial
--    refill against the keeper's max.
--
-- 2. Clamp existing actor_inventory rows that exceed the declared
--    max in the actor's restock policy. The produce-tick gate
--    prevents NEW overproduction, but legacy inventory rows
--    (built up before maxes existed, or after a max was lowered,
--    or from non-tick paths) stay above cap until something burns
--    them down. Observed today: John Ellis at bread=22 against
--    max=15, cheese=27 with no buy max at all. This is the "60
--    bread" symptom; the world should match the model.
--
-- The clamp only touches items that have an explicit max in the
-- actor's restock policy. Items with no policy entry (rare
-- inventoried items, lodging stays, etc.) are unaffected.

BEGIN;

-- 1. Stew → output_qty=1 (was 10). Keep throughput at 5/h:
--    rate_qty=5 + rate_per_hours=1 = 5 minted bowls per hour.
UPDATE item_recipe
   SET output_qty     = 1,
       rate_qty       = 5,
       rate_per_hours = 1
 WHERE output_item = 'stew';

-- 2. Clamp overstock to declared max. Pulls (actor, item, max)
--    from every restock entry that has a max field (either source).
--    Buy entries that use the legacy `target` field are also
--    honored — see engine RestockEntry.Cap() for the precedence rule.
WITH actor_caps AS (
    SELECT aa.actor_id,
           e->>'item' AS item_kind,
           COALESCE((e->>'max')::int, (e->>'target')::int, 0) AS cap
      FROM actor_attribute aa,
           jsonb_array_elements(aa.params->'restock') AS e
     WHERE aa.params ? 'restock'
       AND (e ? 'max' OR e ? 'target')
)
UPDATE actor_inventory ai
   SET quantity = ac.cap
  FROM actor_caps ac
 WHERE ai.actor_id = ac.actor_id
   AND ai.item_kind = ac.item_kind
   AND ac.cap > 0
   AND ai.quantity > ac.cap;

COMMIT;
