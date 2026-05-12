-- ZBBS-HOME-275 — give the blacksmith a real income stream by making
-- John's stew production consume a skillet every 30 meals.
--
-- Problem (2026-05-12): Ezekiel Crane works at the Blacksmith but has
-- zero sales lifetime — he holds 3 hammers, 2 axes, 8 horseshoes, 40
-- nails, but no NPC has any need-driven reason to buy any of them.
-- Meanwhile he spends 5-10 coins/day on food + lodging. He runs at
-- coins=0 and resorts to begging ("I'll work for food").
--
-- Fix: introduce a `skillet` item_kind that John (the tavernkeeper)
-- requires for stew production. The skillet wears 1 per 30 stews
-- served, expressed without a new "wear" mechanism by restructuring
-- the stew recipe as a 30-output batch with skillet as one of the
-- inputs at qty=1. Other inputs (meat/milk/carrots/water) scale to
-- qty=30 each, so the effective per-stew rate is unchanged for them.
--
-- Schema constraint: the recipe model has integer-only input qty and
-- atomic batch executions (one batch = consume one of each input × qty,
-- produce output_qty outputs). Fractional input consumption is
-- expressed via batch amortization rather than per-input rates. This
-- migration leans on that existing pattern.
--
-- The per-instance "John's skillet is 50% worn — order another"
-- perception nuance is NOT in this migration. That requires
-- instance-identified inventory which the current `(actor, item_kind,
-- qty)` shape can't express. Deferred to the in-memory engine rewrite
-- where per-instance state is natural — design captured in
-- `shared/notes/design/in-memory-rewrite-durable-goods-wear`.

BEGIN;

-- 1. New item_kind: skillet. Tool category, portable.
INSERT INTO item_kind (name, display_label, category, sort_order, capabilities)
VALUES ('skillet', 'Skillet', 'tool', 305, ARRAY['portable'])
ON CONFLICT (name) DO NOTHING;

-- 2. Skillet recipe: terminator for v1 (no iron input yet — pretend the
-- blacksmith pulls scrap from the forge bin). Produces slowly: 1 every
-- 3 hours, capped at 5 in stock per actor's restock policy. Wholesale
-- 5 coins, retail 10. At John's 4 batches/day × 1 skillet/batch = 4
-- skillets/day demanded, Ezekiel produces 8/day so the pipeline stays
-- topped up.
INSERT INTO item_recipe (output_item, output_qty, rate_qty, rate_per_hours, inputs, wholesale_price, retail_price)
VALUES ('skillet', 1, 1, 3, '[]'::jsonb, 5, 10)
ON CONFLICT (output_item) DO NOTHING;

-- 3. Restructure stew recipe. output_qty/rate_qty/rate_per_hours move
-- from 1/5/1 to 30/30/6 so effective throughput is unchanged at 5
-- stews/hour but every batch is 30 outputs that share 1 skillet. Other
-- inputs scale to 30 each so per-stew consumption of meat/milk/carrots/
-- water is unchanged on average.
UPDATE item_recipe
   SET output_qty = 30,
       rate_qty = 30,
       rate_per_hours = 6,
       inputs = '[
         {"qty": 30, "item": "meat"},
         {"qty": 30, "item": "water"},
         {"qty": 30, "item": "milk"},
         {"qty": 30, "item": "carrots"},
         {"qty": 1, "item": "skillet"}
       ]'::jsonb,
       updated_at = NOW()
 WHERE output_item = 'stew';

-- 4. Ezekiel's blacksmith attribute: add a produce restock entry for
-- skillet. He keeps up to 5 in stock. Without this entry his blacksmith
-- role params is `{}` and the produce_tick never sees him.
UPDATE actor_attribute
   SET params = '{"restock": [{"max": 5, "item": "skillet", "source": "produce"}]}'::jsonb
 WHERE actor_id = (SELECT id FROM actor WHERE display_name = 'Ezekiel Crane')
   AND slug = 'blacksmith';

-- 5. John's tavernkeeper restock policy: bump stew max to 60. Why 60
-- and not 30: produce_tick fires atomic batches, with
-- executionsByCap = headroom / output_qty. With max=30 and output_qty
-- =30, headroom < 30 whenever stew > 0, and the batch never fires
-- unless inventory drains to exactly 0 first. Setting max=60 means
-- headroom >= 30 whenever current stew <= 30 — so a batch can fire
-- any time John is at half-or-below stock, and steady-state inventory
-- oscillates between ~0 and 60. Same idea applies to bumping ingredient
-- buy targets to 30 (he needs 30 of each per batch) and adding skillet
-- to the buy list.
UPDATE actor_attribute
   SET params = '{"restock": [
       {"max": 60, "item": "stew", "source": "produce"},
       {"max": 30, "item": "water", "source": "produce"},
       {"max": 20, "item": "ale", "source": "produce"},
       {"max": 15, "item": "bread", "source": "produce"},
       {"item": "cheese", "source": "buy", "target": 8},
       {"item": "meat", "source": "buy", "target": 30},
       {"item": "milk", "source": "buy", "target": 30},
       {"item": "carrots", "source": "buy", "target": 30},
       {"item": "skillet", "source": "buy", "target": 2}
   ]}'::jsonb
 WHERE actor_id = (SELECT id FROM actor WHERE display_name = 'John Ellis')
   AND slug = 'tavernkeeper';

-- 6. Seed skillet inventories: John starts with 1 (his current
-- working skillet), Ezekiel starts with 3 (initial stock to sell).
-- GREATEST upsert so re-running the migration tops up a zero-quantity
-- row rather than leaving it at 0.
INSERT INTO actor_inventory (actor_id, item_kind, quantity)
VALUES
    ((SELECT id FROM actor WHERE display_name = 'John Ellis'), 'skillet', 1),
    ((SELECT id FROM actor WHERE display_name = 'Ezekiel Crane'), 'skillet', 3)
ON CONFLICT (actor_id, item_kind) DO UPDATE
    SET quantity = GREATEST(actor_inventory.quantity, EXCLUDED.quantity);

-- 7. Smooth the deploy: top up John's stew ingredients to the new
-- batch size so the first batch fires as soon as the time gate
-- permits (see step 8). Also seed stew=30 directly so John has
-- something to serve during the ~6-hour wait until the time gate
-- first allows a batch. Without the stew seed John would run out
-- (current inventory 0) and serve nothing for ~6 hours, then 30
-- arrive at once. With the seed, he serves down from 30, the time
-- gate eventually allows a batch when stew has drained below the
-- max-30 headroom threshold. The steady-state demand is unchanged;
-- this is one-time transition state.
INSERT INTO actor_inventory (actor_id, item_kind, quantity)
VALUES
    ((SELECT id FROM actor WHERE display_name = 'John Ellis'), 'meat', 30),
    ((SELECT id FROM actor WHERE display_name = 'John Ellis'), 'milk', 30),
    ((SELECT id FROM actor WHERE display_name = 'John Ellis'), 'carrots', 30),
    ((SELECT id FROM actor WHERE display_name = 'John Ellis'), 'water', 30),
    ((SELECT id FROM actor WHERE display_name = 'John Ellis'), 'stew', 30)
ON CONFLICT (actor_id, item_kind) DO UPDATE
    SET quantity = GREATEST(actor_inventory.quantity, EXCLUDED.quantity);

-- 8. Reset John's stew produce anchor to NOW so the new 720s/unit
-- rate starts accumulating from migration time. With output_qty=30,
-- the time gate requires 30 unitsOwed (30 * 720s = 6 hours of
-- accrual) before one batch can fire — the seed in step 7 bridges
-- this gap so John always has stew to serve. Upsert handles the
-- case where John has no actor_produce_state row yet (the row is
-- normally created lazily on first produce_tick observation).
INSERT INTO actor_produce_state (actor_id, item_kind, last_produced_at)
VALUES
    ((SELECT id FROM actor WHERE display_name = 'John Ellis'), 'stew', NOW())
ON CONFLICT (actor_id, item_kind) DO UPDATE
    SET last_produced_at = EXCLUDED.last_produced_at;

COMMIT;
