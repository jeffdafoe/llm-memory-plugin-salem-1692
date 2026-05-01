-- ZBBS-091: Inventory and trade — Phase 1.
--
-- Items become first-class. Each actor carries a quantity-stacked
-- inventory of typed items; new agent actions (buy / consume) enable
-- supply-chain commerce alongside the existing tavern-style pay() verb.
--
-- Two tables:
--
--   item_kind        — lookup. One row per item type the world knows
--                      about. Carries display label, category, default
--                      price, and (for consumables) which actor need
--                      one unit satisfies and by how much.
--
--   actor_inventory  — per-actor quantity-stacked inventory. Composite
--                      PK (actor_id, item_kind). Quantity > 0 — zero
--                      rows are deleted to keep perception text clean.
--
-- pay() stays unchanged. Tavern interactions ("pay 2 for ale") continue
-- to work via the existing free-text keyword vocabulary; inventory is
-- a new path for commerce that take-home implies (merchant flour,
-- bakery bread, blacksmith tools later).
--
-- Design doc: shared/notes/codebase/salem/inventory-and-trade.

BEGIN;

CREATE TABLE item_kind (
    name                 VARCHAR(32) PRIMARY KEY,
    display_label        VARCHAR(64) NOT NULL,
    -- 'food' | 'drink' | 'material' | 'craft'. Free string for now —
    -- formal lookup table when categories actually drive behavior.
    category             VARCHAR(32) NOT NULL,
    -- Per-unit price in coins. Static at item-kind level; per-actor
    -- price overrides (tavernkeeper sets ale price) come later.
    price                SMALLINT NOT NULL CHECK (price >= 0),
    -- Which actor need a unit of this item satisfies when consumed,
    -- and by how much. Both NULL for non-consumables (materials).
    -- The pair is enforced together by a CHECK below.
    satisfies_attribute  VARCHAR(32) NULL REFERENCES refresh_attribute(name) ON UPDATE CASCADE,
    satisfies_amount     SMALLINT NULL CHECK (satisfies_amount IS NULL OR satisfies_amount > 0),
    -- UI sort order. Categories cluster in the picker via this.
    sort_order           SMALLINT NOT NULL DEFAULT 0,
    -- Both columns or neither — partial state would mean "satisfies
    -- some attribute by an unknown amount" which doesn't make sense.
    CONSTRAINT item_kind_satisfies_pair
        CHECK ((satisfies_attribute IS NULL) = (satisfies_amount IS NULL))
);

-- Seed: tavern fare + materials needed for Phase 2 recipes.
-- satisfies_amount is positive — engine negates at the point of
-- application via applyConsumption. Mirrors how the refresh row's
-- "restores per use" reads as positive in the API even though
-- object_refresh.amount is stored negative.
INSERT INTO item_kind (name, display_label, category, price, satisfies_attribute, satisfies_amount, sort_order) VALUES
    ('ale',     'Ale',     'drink',    2, 'thirst',  8,  10),
    ('water',   'Water',   'drink',    0, 'thirst',  4,  20),
    ('milk',    'Milk',    'drink',    1, 'thirst',  6,  30),
    ('stew',    'Stew',    'food',     3, 'hunger',  12, 110),
    ('bread',   'Bread',   'food',     2, 'hunger',  8,  120),
    ('cheese',  'Cheese',  'food',     2, 'hunger',  6,  130),
    ('berries', 'Berries', 'food',     1, 'hunger',  4,  140),
    ('meat',    'Meat',    'food',     4, 'hunger',  10, 150),
    -- Materials. NULL/NULL satisfies — nothing happens if you "consume"
    -- raw wheat. Recipes (Phase 2) consume them as inputs to produce
    -- food.
    ('wheat',   'Wheat',   'material', 1, NULL, NULL, 210),
    ('flour',   'Flour',   'material', 2, NULL, NULL, 220),
    ('iron',    'Iron',    'material', 5, NULL, NULL, 230);

CREATE TABLE actor_inventory (
    actor_id   UUID NOT NULL REFERENCES actor(id) ON DELETE CASCADE,
    item_kind  VARCHAR(32) NOT NULL REFERENCES item_kind(name) ON UPDATE CASCADE,
    quantity   SMALLINT NOT NULL CHECK (quantity > 0),
    PRIMARY KEY (actor_id, item_kind)
);

-- Seed initial NPC inventories so the tavern is stocked and the
-- merchant has something to sell on the first arrival of an inventory-
-- aware engine. Uses display_name lookup so the migration doesn't
-- assume specific UUIDs. Skips silently if a name doesn't resolve —
-- INSERT ... SELECT FROM actor WHERE returns zero rows in that case.
INSERT INTO actor_inventory (actor_id, item_kind, quantity)
SELECT a.id, v.kind, v.qty
  FROM actor a
  JOIN (VALUES
    ('John Ellis',     'ale',     10),
    ('John Ellis',     'stew',    5),
    ('John Ellis',     'bread',   10),
    ('Josiah Thorne',  'bread',   5),
    ('Josiah Thorne',  'milk',    5),
    ('Josiah Thorne',  'cheese',  3),
    ('Prudence Ward',  'berries', 8),
    ('Prudence Ward',  'water',   10)
  ) AS v(name, kind, qty) ON LOWER(a.display_name) = LOWER(v.name);

COMMIT;
