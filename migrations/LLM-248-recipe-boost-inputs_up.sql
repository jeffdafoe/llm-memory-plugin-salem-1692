-- LLM-248: optional booster inputs on recipes.
--
-- boost_inputs is a JSONB array of {item, qty, bonus_qty} objects — the
-- optional-input mirror of the required `inputs` array. At each produce-tick
-- execution, a producer holding `qty` of a booster consumes it and mints
-- `bonus_qty` extra output (cap-clamped); holding none leaves base production
-- untouched. First consumer: the LLM-83 dairy edge (milk boosted by sage).
--
-- Same posture as `inputs`: engine-validated Go-side (item existence, positive
-- quantities, no overlap with required inputs); the DB enforces only the array
-- shape.

BEGIN;

ALTER TABLE item_recipe
    ADD COLUMN boost_inputs jsonb NOT NULL DEFAULT '[]';

ALTER TABLE item_recipe
    ADD CONSTRAINT item_recipe_boost_inputs_array
    CHECK (jsonb_typeof(boost_inputs) = 'array');

COMMIT;
