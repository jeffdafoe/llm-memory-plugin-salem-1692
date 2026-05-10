BEGIN;

-- Revert stew recipe shape.
UPDATE item_recipe
   SET output_qty     = 10,
       rate_qty       = 10,
       rate_per_hours = 2
 WHERE output_item = 'stew';

-- The inventory clamp is data-destructive and not reversible.
-- (Recovering the trimmed quantities would require an audit log
-- we don't keep.) The schema isn't changed, so no down action.

COMMIT;
