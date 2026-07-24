-- LLM-511 down: drop the speed-inputs column. The shovel's interim LLM-442
-- boost_inputs [{iron,1,+1}] is NOT restored here — the down path is a schema
-- rollback, and re-deriving the pre-511 economic data is out of scope (matches
-- the LLM-248 down posture, which only drops its column).

BEGIN;

ALTER TABLE item_recipe
    DROP CONSTRAINT IF EXISTS item_recipe_speed_inputs_array;

ALTER TABLE item_recipe
    DROP COLUMN IF EXISTS speed_inputs;

COMMIT;
