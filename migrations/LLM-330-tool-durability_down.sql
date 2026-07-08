-- LLM-330 down: drop per-use tool durability.

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
