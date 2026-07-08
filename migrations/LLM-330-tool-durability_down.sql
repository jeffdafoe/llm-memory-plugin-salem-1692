-- LLM-330 down: drop per-use tool durability.
--
-- The fried_meat input strip removes EVERY skillet entry, not only one this
-- migration's up added — acceptable because no environment carried a skillet
-- input on fried_meat before LLM-330 (it was deliberately shipped without one
-- in the LLM-325 fallout, pending this ticket).

BEGIN;

UPDATE item_recipe
   SET inputs = (
       SELECT COALESCE(jsonb_agg(elem), '[]'::jsonb)
         FROM jsonb_array_elements(inputs) AS elem
        WHERE elem->>'item' <> 'skillet'
   )
 WHERE output_item = 'fried_meat';

ALTER TABLE actor_inventory DROP COLUMN uses_left;

ALTER TABLE item_kind DROP COLUMN durability_uses;

COMMIT;
