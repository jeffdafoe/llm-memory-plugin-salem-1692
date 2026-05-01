-- Rollback for ZBBS-093. Re-collapses to meal/drink generics and
-- drops the capabilities column. Inventories are best-effort SUM-
-- collapsed back the same way ZBBS-092 originally did; the eight
-- specific rows go away.

BEGIN;

INSERT INTO item_kind (name, display_label, category, satisfies_attribute, satisfies_amount, sort_order) VALUES
    ('meal',  'Meal',  'food',  'hunger', 10, 100),
    ('drink', 'Drink', 'drink', 'thirst', 6,  50);

INSERT INTO actor_inventory (actor_id, item_kind, quantity)
SELECT actor_id, 'meal', SUM(quantity)::smallint
  FROM actor_inventory
 WHERE item_kind IN ('stew', 'bread', 'cheese', 'berries', 'meat')
 GROUP BY actor_id
ON CONFLICT (actor_id, item_kind) DO UPDATE
   SET quantity = actor_inventory.quantity + EXCLUDED.quantity;

INSERT INTO actor_inventory (actor_id, item_kind, quantity)
SELECT actor_id, 'drink', SUM(quantity)::smallint
  FROM actor_inventory
 WHERE item_kind IN ('ale', 'water', 'milk')
 GROUP BY actor_id
ON CONFLICT (actor_id, item_kind) DO UPDATE
   SET quantity = actor_inventory.quantity + EXCLUDED.quantity;

DELETE FROM actor_inventory
 WHERE item_kind IN ('stew', 'bread', 'cheese', 'berries', 'meat',
                     'ale',  'water', 'milk');

DELETE FROM item_kind
 WHERE name IN ('stew', 'bread', 'cheese', 'berries', 'meat',
                'ale',  'water', 'milk');

ALTER TABLE item_kind DROP COLUMN capabilities;

COMMIT;
