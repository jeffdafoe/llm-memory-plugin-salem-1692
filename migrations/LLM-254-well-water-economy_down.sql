-- LLM-254 rollback: undo the Well water economy. Restores the single eat+pick water
-- row on the Well and strips Josiah's water-forage config. Reverses each up step.
--
-- Leaves the forage_range attribute_definition in place -- that catalog row is
-- LLM-253's, not this migration's. Only Josiah's grant is removed.

BEGIN;

-- Reverse 2b: drop the forage-water restock entry from Josiah's merchant params,
-- rebuilding the array WITHOUT it so his existing buy entries stay intact. An empty
-- result collapses to [] rather than NULL.
UPDATE actor_attribute
   SET params = jsonb_set(
       params,
       '{restock}',
       COALESCE((
           SELECT jsonb_agg(elem)
           FROM jsonb_array_elements(params->'restock') elem
           WHERE NOT (elem @> '{"item": "water", "source": "forage"}')
       ), '[]'::jsonb)
   )
 WHERE actor_id = '019dcac2-e78a-715e-91b7-101f339b0891'
   AND slug = 'merchant'
   AND params ? 'restock';

-- Reverse 2a: remove Josiah's forage_range grant.
DELETE FROM actor_attribute
 WHERE actor_id = '019dcac2-e78a-715e-91b7-101f339b0891'
   AND slug = 'forage_range';

-- Reverse 1b: remove the yield-only water row.
DELETE FROM object_refresh
 WHERE object_id = '019d79ef-d9df-73d7-967a-dc202ceaf624'
   AND attribute IS NULL
   AND gather_item = 'water';

-- Reverse 1a: restore gather_item=water on the thirst row (back to eat+pick).
UPDATE object_refresh
   SET gather_item = 'water'
 WHERE object_id = '019d79ef-d9df-73d7-967a-dc202ceaf624'
   AND attribute = 'thirst';

COMMIT;
