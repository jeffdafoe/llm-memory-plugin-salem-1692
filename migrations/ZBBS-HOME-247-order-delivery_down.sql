BEGIN;
DROP TABLE IF EXISTS actor_delivery_in_progress;
ALTER TABLE item_recipe DROP COLUMN IF EXISTS wholesale_price;
ALTER TABLE item_recipe DROP COLUMN IF EXISTS retail_price;
COMMIT;
