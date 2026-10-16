-- LLM-797 down: milk is made at Ellis Farm again. Restores Elizabeth's `produce`
-- milk entry and the recipe's sage boost, and drops the cows' gather rows with
-- the column. Run with the engine stopped (actor / actor_attribute are
-- checkpoint-written).

BEGIN;

UPDATE public.actor_attribute
SET params = jsonb_set(params, '{restock}', (
    SELECT jsonb_agg(CASE WHEN e->>'item' = 'milk' THEN jsonb_set(e, '{source}', '"produce"') ELSE e END)
    FROM jsonb_array_elements(params->'restock') e))
WHERE actor_id = '019da6af-c8c9-7eb8-aead-759142785789'  -- Elizabeth Ellis
  AND slug = 'dairykeeper'
  AND params ? 'restock';

UPDATE public.item_recipe
SET boost_inputs = '[{"qty": 1, "item": "sage", "bonus_qty": 4}]'::jsonb, updated_at = now()
WHERE output_item = 'milk';

ALTER TABLE public.actor DROP CONSTRAINT IF EXISTS actor_livestock_yield_object;
ALTER TABLE public.actor DROP COLUMN IF EXISTS livestock_yield;

COMMIT;
