-- ZBBS-155 down: drop gatherable_node table.
--
-- The seeded rows go with the table. No `actor_inventory` rows are
-- destroyed — items already picked up persist (they're regular items,
-- not gatherable-anchored).

BEGIN;
DROP TABLE IF EXISTS gatherable_node;
COMMIT;
