-- ZBBS-092: drop item_kind.price + collapse food/drink rows to generic
-- 'meal' and 'drink'.
--
-- Two related changes that ship together because they share a
-- philosophy: schema captures categories, dialogue captures flavor.
--
-- (1) Static price column goes. Negotiation already worked fine in
-- pay() — buy() now takes an `amount` parameter the same way. Supply-
-- constrained pricing emerges from conversation ("only two bowls left,
-- it'll be 5 today"); the engine doesn't need to know the going rate.
--
-- (2) The seven specific food rows (stew, bread, cheese, berries,
-- meat) and three drink rows (ale, water, milk) collapse into one
-- 'meal' and one 'drink' row. John flavors his stock however he wants
-- in conversation — "supper", "tonight's stew", "fresh bread", "the
-- pottage" — but mechanically the engine moves a meal. Same for
-- drinks. Materials (wheat, flour, iron) stay specific because Phase
-- 2 recipes need to differentiate "what goes in" from "what comes
-- out": flour → meal, but flour and meal are different rows.
--
-- Existing actor_inventory rows roll up by SUM. John's ale x10 + stew
-- x5 + bread x10 becomes drink x10 + meal x15. No data loss in
-- aggregate; flavor was never in the database to begin with.

BEGIN;

ALTER TABLE item_kind DROP COLUMN price;

-- New generic rows. satisfies_amount is the average of what the
-- specific items used to satisfy: meal lands at 10 (between bread 8
-- and stew 12); drink lands at 6 (between water 4 and ale 8). One
-- value per category — no more "this dish satisfies 12 vs that one 8."
-- Loss of differentiation, gain of simplicity.
INSERT INTO item_kind (name, display_label, category, satisfies_attribute, satisfies_amount, sort_order) VALUES
    ('meal',  'Meal',  'food',  'hunger', 10, 100),
    ('drink', 'Drink', 'drink', 'thirst', 6,  50);

-- Roll up existing food inventories into 'meal'. ON CONFLICT in case
-- 'meal' was already seeded into someone's inventory (it wasn't, but
-- defense in depth), summing instead of clobbering.
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

-- Drop the now-orphaned old rows from actor_inventory, then from
-- item_kind. The FK on item_kind doesn't ON DELETE CASCADE, so
-- inventory must clear first.
DELETE FROM actor_inventory
 WHERE item_kind IN ('stew', 'bread', 'cheese', 'berries', 'meat',
                     'ale',  'water', 'milk');

DELETE FROM item_kind
 WHERE name IN ('stew', 'bread', 'cheese', 'berries', 'meat',
                'ale',  'water', 'milk');

COMMIT;
