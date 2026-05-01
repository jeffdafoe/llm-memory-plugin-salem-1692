-- ZBBS-093: walk back the ZBBS-092 food/drink collapse + add an item
-- capabilities array.
--
-- ZBBS-092 collapsed all foods into one 'meal' row and all drinks
-- into one 'drink' row, on the theory that the LLMs would handle
-- naming variation in dialogue. That works for raw ingestion but
-- breaks Phase 2 recipes: wheat → flour → bread is only meaningful if
-- bread is a distinct row from stew. With recipes incoming the
-- collapse removes information the schema needs.
--
-- Restoration: re-add the eight specific item rows from ZBBS-091
-- (ale, water, milk, stew, bread, cheese, berries, meat). Drop the
-- now-redundant 'meal' and 'drink' generic rows. Restore actor
-- inventories to their pre-ZBBS-092 contents — quantities are known
-- from the seed (no real trading happened in the brief deploy
-- window). The price column stays dropped from ZBBS-092; pay() and
-- buy() continue to use negotiated `amount` values.
--
-- New: capabilities TEXT[] column. Each item carries a set of feature
-- tags. Currently one tag — 'portable' — gates whether buy() will
-- transfer the item to the buyer's pack or reject (you can't carry
-- a hot bowl of stew home from the tavern). Future tags: 'forageable',
-- 'perishable', etc. Tag-based instead of a portable boolean per the
-- project rule that capability flags are JSON arrays / sets, not
-- per-feature columns.
--
-- Capabilities seed:
--   stew, water       -> {}             (consumed at source only)
--   ale, milk, bread,
--   cheese, berries,
--   meat              -> {portable}
--
-- Water is non-portable for now — no flasks/canteens modeled.

BEGIN;

ALTER TABLE item_kind ADD COLUMN capabilities TEXT[] NOT NULL DEFAULT '{}';

-- Restore the eight specific item rows. Capabilities seeded inline.
INSERT INTO item_kind (name, display_label, category, satisfies_attribute, satisfies_amount, sort_order, capabilities) VALUES
    ('ale',     'Ale',     'drink',    'thirst', 8,  10,  ARRAY['portable']),
    ('water',   'Water',   'drink',    'thirst', 4,  20,  ARRAY[]::TEXT[]),
    ('milk',    'Milk',    'drink',    'thirst', 6,  30,  ARRAY['portable']),
    ('stew',    'Stew',    'food',     'hunger', 12, 110, ARRAY[]::TEXT[]),
    ('bread',   'Bread',   'food',     'hunger', 8,  120, ARRAY['portable']),
    ('cheese',  'Cheese',  'food',     'hunger', 6,  130, ARRAY['portable']),
    ('berries', 'Berries', 'food',     'hunger', 4,  140, ARRAY['portable']),
    ('meat',    'Meat',    'food',     'hunger', 10, 150, ARRAY['portable']);

-- Update existing material rows so they're explicitly portable.
UPDATE item_kind SET capabilities = ARRAY['portable']
 WHERE name IN ('wheat', 'flour', 'iron');

-- Restore inventory to ZBBS-091 seed contents. Drop existing
-- meal/drink rows for the seeded actors then re-insert the original
-- split.
DELETE FROM actor_inventory ai
 USING actor a
 WHERE ai.actor_id = a.id
   AND LOWER(a.display_name) IN (LOWER('John Ellis'), LOWER('Josiah Thorne'), LOWER('Prudence Ward'))
   AND ai.item_kind IN ('meal', 'drink');

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

-- Drop the generics now that nothing references them.
DELETE FROM item_kind WHERE name IN ('meal', 'drink');

COMMIT;
