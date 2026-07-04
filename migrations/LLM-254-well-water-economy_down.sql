-- LLM-254 rollback: undo the Well water economy. Restores the single eat+pick water
-- row on the Well and strips Josiah's water-forage config. Reverses each up step,
-- matching the EXACT shape this migration created so it never clobbers unrelated or
-- pre-existing rows.
--
-- Leaves the forage_range attribute_definition in place -- that catalog row is
-- LLM-253's, not this migration's. Josiah had no forage_range grant before this
-- migration (verified), so removing his grant is migration-owned and correct.

BEGIN;

-- Reverse 2b: drop ONLY the exact `forage water` entry this migration added,
-- rebuilding the array without it so his other restock entries stay intact. The
-- jsonb_typeof guard keeps a malformed non-array restock from erroring in
-- jsonb_array_elements. An empty result collapses to [] rather than NULL.
UPDATE actor_attribute
   SET params = jsonb_set(
       params,
       '{restock}',
       COALESCE((
           SELECT jsonb_agg(elem)
           FROM jsonb_array_elements(
               CASE WHEN jsonb_typeof(params->'restock') = 'array'
                    THEN params->'restock' ELSE '[]'::jsonb END
           ) elem
           WHERE elem <> '{"item": "water", "source": "forage", "max": 20}'::jsonb
       ), '[]'::jsonb)
   )
 WHERE actor_id = '019dcac2-e78a-715e-91b7-101f339b0891'
   AND slug = 'merchant'
   AND params ? 'restock';

-- Reverse 2a: remove Josiah's forage_range grant (migration-owned; see header).
DELETE FROM actor_attribute
 WHERE actor_id = '019dcac2-e78a-715e-91b7-101f339b0891'
   AND slug = 'forage_range';

-- Reverse 1b: remove the yield-only water row, matched on the FULL shape this
-- migration inserted so drift / an unrelated water yield row is never deleted.
DELETE FROM object_refresh
 WHERE object_id = '019d79ef-d9df-73d7-967a-dc202ceaf624'
   AND attribute IS NULL
   AND gather_item = 'water'
   AND amount = 0
   AND available_quantity = 20
   AND max_quantity = 20
   AND refresh_mode = 'periodic'
   AND refresh_period_hours = 6;

-- Reverse 1a: restore gather_item=water on the thirst row (back to eat+pick). Matched
-- on the exact post-split shape (thirst / -8 / gather_item NULL).
UPDATE object_refresh
   SET gather_item = 'water'
 WHERE object_id   = '019d79ef-d9df-73d7-967a-dc202ceaf624'
   AND attribute   = 'thirst'
   AND amount      = -8
   AND gather_item IS NULL;

COMMIT;
