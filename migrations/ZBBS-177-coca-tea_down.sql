-- ZBBS-177 down — remove coca tea
--
-- ON DELETE CASCADE on item_satisfies and actor_inventory FKs means
-- DELETEing the item_kind row also clears the satisfies row and any
-- inventory holdings. Keep the explicit DELETEs anyway for clarity in
-- the migration log.

BEGIN;

DELETE FROM actor_inventory WHERE item_kind = 'coca_tea';
DELETE FROM item_satisfies  WHERE item_kind = 'coca_tea';
DELETE FROM item_kind       WHERE name      = 'coca_tea';

COMMIT;
