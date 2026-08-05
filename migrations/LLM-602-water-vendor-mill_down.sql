-- LLM-602 down: put the water-vendor role back on the General Store.
--
-- Reverses the two things the up migration actually CREATED, and deliberately
-- leaves alone the one thing it did not.
--
-- WHY THE ASYMMETRY. LLM-254's own down is the cautionary case this ticket was
-- filed on: it does an unconditional
--
--     DELETE FROM actor_attribute WHERE actor_id = <josiah> AND slug = 'forage_range'
--
-- with no check that its up created that row, which its code review flagged at the
-- time ("make down migration avoid deleting pre-existing Josiah forage water
-- restock/grant state that up did not create"). Joseph's `forage water` restock
-- entry pre-dates this migration — it was added live through the umbilical on
-- 2026-08-05 — so removing it here would repeat exactly that mistake against a
-- different actor. It is left in place. Without the grant it is inert, which is
-- precisely the harmless state Josiah's entry has been sitting in.
--
-- Joseph's `forage_range` grant IS removed: it does not exist in prod today, so
-- this migration is unambiguously its creator.
--
-- actor_attribute is CHECKPOINT-WRITTEN — apply engine-stopped.

BEGIN;

-- 1. Flip Josiah's water entry back to `forage`. Element-wise rebuild, preserving
--    his other entries and their order. Guarded on the entry currently being `buy`,
--    so re-running changes nothing.
UPDATE actor_attribute
   SET params = jsonb_set(
       params,
       '{restock}',
       (SELECT jsonb_agg(
                   CASE WHEN elem->>'item' = 'water'
                        THEN jsonb_set(elem, '{source}', '"forage"'::jsonb)
                        ELSE elem END
                   ORDER BY ord)
          FROM jsonb_array_elements(params->'restock') WITH ORDINALITY AS t(elem, ord))
   )
 WHERE actor_id = '019dcac2-e78a-715e-91b7-101f339b0891'
   AND slug = 'merchant'
   AND jsonb_typeof(params->'restock') = 'array'
   AND params->'restock' @> '[{"item": "water", "source": "buy"}]'::jsonb;

-- 2. Remove Joseph's forage_range grant (created by this migration's up).
DELETE FROM actor_attribute
 WHERE actor_id = '019da6b7-a853-79fb-91eb-645e5d9915c1'
   AND slug = 'forage_range';

-- NOT REMOVED, deliberately: Joseph's `{"item":"water","source":"forage","max":20}`
-- restock entry. See the header — it pre-dates this migration.

COMMIT;
