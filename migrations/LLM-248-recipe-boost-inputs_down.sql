-- LLM-248 down: drop the optional booster-inputs column.

BEGIN;

ALTER TABLE item_recipe
    DROP CONSTRAINT IF EXISTS item_recipe_boost_inputs_array;

ALTER TABLE item_recipe
    DROP COLUMN IF EXISTS boost_inputs;

COMMIT;
