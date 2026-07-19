-- LLM-474 down: drop the state-keyed booster column.
--
-- Dropping the column discards the authored hearth_lit rows with it; the up
-- migration re-authors them from scratch, so nothing needs preserving here.

BEGIN;

ALTER TABLE item_recipe
    DROP CONSTRAINT IF EXISTS item_recipe_boost_state_array;

ALTER TABLE item_recipe
    DROP COLUMN IF EXISTS boost_state;

COMMIT;
