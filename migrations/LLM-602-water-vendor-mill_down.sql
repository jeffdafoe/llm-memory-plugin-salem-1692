-- LLM-602 down: put the water-vendor role back on the General Store.
--
-- Reverses the two things the up migration CREATED, and deliberately leaves alone
-- the one thing it did not.
--
-- WHY THE ASYMMETRY. LLM-254's own down is the cautionary case this ticket was filed
-- on: it does an unconditional
--
--     DELETE FROM actor_attribute WHERE actor_id = <josiah> AND slug = 'forage_range'
--
-- with no check that its up created that row, which its code review flagged at the
-- time ("make down migration avoid deleting pre-existing Josiah forage water
-- restock/grant state that up did not create"). Joseph's `forage water` restock
-- entry pre-dates this migration — it was added live through the umbilical on
-- 2026-08-05 — so removing it here would repeat that mistake against a different
-- actor. It is left in place. Without the grant it is inert, which is precisely the
-- harmless state Josiah's entry sat in for the month this ticket describes.
--
-- PRECONDITION ON THE GRANT DELETE, stated rather than pretended away. Step 2 removes
-- Joseph's `forage_range` row unconditionally. That is provenance-safe ONLY against
-- the known baseline in which Joseph has no such grant before the up runs (verified
-- in prod 2026-08-05: `forage_range` was held by Prudence Ward alone). It is NOT safe
-- against an environment where an operator has since granted Joseph forage_range for
-- an unrelated reason — this down would revoke it. A plain existence check cannot
-- distinguish migration-created from operator-created state, and `params` is `{}` in
-- both cases, so no predicate fixes this; carrying a provenance marker would be more
-- machinery than a two-actor migration warrants (code_review raised the risk; this is
-- the documented precondition it asked for as the minimum). If Joseph is ever granted
-- forage_range independently, review this file before running it.
--
-- actor_attribute is CHECKPOINT-WRITTEN — apply engine-stopped.

BEGIN;

-- 1. Flip Josiah's water entry back to `forage`. Element-wise rebuild, preserving his
--    other entries and their order. The CASE tests item AND source so a water entry
--    already on `forage` is not rewritten; the outer WHERE only proves at least one
--    `buy` entry exists (code_review). Guarded on `buy`, so re-running changes nothing.
UPDATE actor_attribute
   SET params = jsonb_set(
       params,
       '{restock}',
       (SELECT jsonb_agg(
                   CASE WHEN elem->>'item' = 'water' AND elem->>'source' = 'buy'
                        THEN jsonb_set(elem, '{source}', '"forage"'::jsonb)
                        ELSE elem END
                   ORDER BY ord)
          FROM jsonb_array_elements(params->'restock') WITH ORDINALITY AS t(elem, ord))
   )
 WHERE actor_id = '019dcac2-e78a-715e-91b7-101f339b0891'
   AND slug = 'merchant'
   AND jsonb_typeof(params->'restock') = 'array'
   AND params->'restock' @> '[{"item": "water", "source": "buy"}]'::jsonb;

-- 2. Remove Joseph's forage_range grant. See the precondition in the header.
DELETE FROM actor_attribute
 WHERE actor_id = '019da6b7-a853-79fb-91eb-645e5d9915c1'
   AND slug = 'forage_range';

-- NOT REMOVED, deliberately: Joseph's `{"item":"water","source":"forage","max":20}`
-- restock entry. See the header.

-- Postconditions. Without these the down can COMMIT having deleted Joseph's grant
-- while silently failing to flip Josiah — his policy having changed, gone malformed,
-- or disappeared — leaving the village with no water vendor at all (code_review).
-- Deliberately absent: any assertion about Joseph's water restock entry, since
-- preserving that pre-existing row is the point.
DO $$
DECLARE
    joseph   constant uuid  := '019da6b7-a853-79fb-91eb-645e5d9915c1';
    josiah   constant uuid  := '019dcac2-e78a-715e-91b7-101f339b0891';
    want_jth constant jsonb := '{"item": "water", "source": "forage", "max": 20}';
    n        int;
    got      jsonb;
BEGIN
    IF NOT EXISTS (SELECT 1 FROM actor) THEN
        RETURN;  -- schema-only integration DB
    END IF;

    IF EXISTS (SELECT 1 FROM actor_attribute WHERE actor_id = joseph AND slug = 'forage_range') THEN
        RAISE EXCEPTION 'LLM-602 down: Joseph forage_range grant was not removed';
    END IF;

    -- Count and fetch are separate statements: there is no min(jsonb) aggregate, and
    -- asserting the count FIRST is what makes the bare SELECT INTO below single-row.
    SELECT count(*) INTO n
      FROM actor_attribute aa, jsonb_array_elements(aa.params->'restock') e
     WHERE aa.actor_id = josiah AND aa.slug = 'merchant' AND e->>'item' = 'water';
    IF n <> 1 THEN
        RAISE EXCEPTION 'LLM-602 down: Josiah must carry exactly 1 water restock entry, found %', n;
    END IF;
    SELECT e INTO got
      FROM actor_attribute aa, jsonb_array_elements(aa.params->'restock') e
     WHERE aa.actor_id = josiah AND aa.slug = 'merchant' AND e->>'item' = 'water';
    IF got <> want_jth THEN
        RAISE EXCEPTION 'LLM-602 down: Josiah water entry is %, expected %', got, want_jth;
    END IF;
END $$;

COMMIT;
