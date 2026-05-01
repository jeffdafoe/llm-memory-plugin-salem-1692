-- Rollback for ZBBS-092. Restores price column and reseeds the
-- specific food/drink rows. Existing 'meal' / 'drink' inventories are
-- left alone — there's no honest way to expand "meal x15" back into
-- "stew x5 + bread x10"; the original split was lost at migration
-- time. Operators rolling back should accept that mealtimes lose
-- their differentiation in inventory until manually re-split.

BEGIN;

ALTER TABLE item_kind ADD COLUMN price SMALLINT NOT NULL DEFAULT 0 CHECK (price >= 0);

INSERT INTO item_kind (name, display_label, category, price, satisfies_attribute, satisfies_amount, sort_order) VALUES
    ('ale',     'Ale',     'drink',    2, 'thirst',  8,  10),
    ('water',   'Water',   'drink',    0, 'thirst',  4,  20),
    ('milk',    'Milk',    'drink',    1, 'thirst',  6,  30),
    ('stew',    'Stew',    'food',     3, 'hunger',  12, 110),
    ('bread',   'Bread',   'food',     2, 'hunger',  8,  120),
    ('cheese',  'Cheese',  'food',     2, 'hunger',  6,  130),
    ('berries', 'Berries', 'food',     1, 'hunger',  4,  140),
    ('meat',    'Meat',    'food',     4, 'hunger',  10, 150);

UPDATE item_kind SET price = 1 WHERE name = 'wheat';
UPDATE item_kind SET price = 2 WHERE name = 'flour';
UPDATE item_kind SET price = 5 WHERE name = 'iron';

DELETE FROM item_kind WHERE name IN ('meal', 'drink');

COMMIT;
