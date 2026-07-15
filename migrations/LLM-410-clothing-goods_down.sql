-- LLM-410 (slice 2) rollback: remove the clothing/charm goods, their price
-- anchors, the distributor seed stock, and the item_kind.description column.
--
-- Order respects the foreign keys: the distributor's seeded inventory and the
-- price-anchor recipes go first (actor_inventory -> item_kind is ON UPDATE CASCADE
-- only, NOT on delete, so the inventory rows must clear before the item_kind rows),
-- then the item_kind rows, then the column.
--
-- CAVEAT: this is a dev/pre-trade rollback. Once these goods have been traded,
-- pay_ledger / scene_quote hold references to their item_kind names (ON UPDATE
-- CASCADE, not ON DELETE), so the item_kind delete will fail — you don't unwind a
-- shipped economy. On the schema-only harness and any DB with no clothing trades
-- yet, this reverses cleanly.

BEGIN;

-- 1. Remove the distributor's seeded clothing stock (guarded — no-op if Josiah is
--    absent, e.g. the schema-only harness). Scoped to exactly the seeded kinds so a
--    later hand-added holding of another kind isn't swept.
DELETE FROM actor_inventory
 WHERE actor_id = '019dcac2-e78a-715e-91b7-101f339b0891'
   AND item_kind IN ('coat', 'cloak', 'gown', 'breeches', 'silver_locket');

-- 2. Remove the price anchors. (item_kind's ON DELETE CASCADE to item_recipe would
--    also take these, but delete them explicitly so the intent is clear.)
DELETE FROM item_recipe
 WHERE output_item IN ('breeches','shift','coat','gown','cloak',
                       'whalebone_charm','silver_locket','iron_ward');

-- 3. Remove the goods.
DELETE FROM item_kind
 WHERE name IN ('breeches','shift','coat','gown','cloak',
                'whalebone_charm','silver_locket','iron_ward');

-- 4. Drop the flavor column.
ALTER TABLE item_kind DROP COLUMN IF EXISTS description;

COMMIT;
