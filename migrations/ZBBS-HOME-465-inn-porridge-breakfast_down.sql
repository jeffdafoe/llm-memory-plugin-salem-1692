-- ZBBS-HOME-465 down: remove porridge and Hannah's porridge restock policy.
-- Reverse dependency order: Hannah's inventory + restock first, then the
-- item_satisfies/item_recipe rows (which reference the item_kind), then the
-- item_kind itself.

BEGIN;

-- Remove only the restock key from Hannah's innkeeper params (mirrors the up's
-- jsonb_set, which added just that key — other params, if any, are left intact).
UPDATE actor_attribute
SET params = params - 'restock'
WHERE slug = 'innkeeper'
  AND actor_id = (SELECT id FROM actor WHERE display_name = 'Hannah Boggs');

-- Drop Hannah's porridge stock.
DELETE FROM actor_inventory
WHERE item_kind = 'porridge'
  AND actor_id = (SELECT id FROM actor WHERE display_name = 'Hannah Boggs');

DELETE FROM item_satisfies WHERE item_kind = 'porridge';
DELETE FROM item_recipe    WHERE output_item = 'porridge';
DELETE FROM item_kind      WHERE name = 'porridge';

COMMIT;
