-- LLM-511: speed-booster recipe inputs, and the first consumer — a bar of iron
-- halves shovel forge time.
--
-- speed_inputs is a JSONB array of {item, qty, rate_pct} objects — the rate-side
-- sibling of boost_inputs. Where a boost_input is consumed AT LANDING for extra
-- output, a speed_input is consumed AT START to cut the cycle time: rate_pct 200
-- runs the batch at 2x rate (half the wall time). For the shovel that models
-- shaping an existing bar instead of forging from scrap — the bar just has to be
-- smooshed.
--
-- Same posture as inputs / boost_inputs / boost_state: engine-validated Go-side
-- (item exists, positive qty, rate_pct > 100, not a required input, no duplicate
-- item); the DB enforces only the array shape.
--
-- CRITICAL INVARIANT: a speed_input is elective and must NEVER gate. Every recipe
-- stays producible at base rate with no speed input in hand — holding none just
-- means the slow forge, never a stall (the absorbing-state / liveness rule, same
-- as boost_inputs).

BEGIN;

ALTER TABLE item_recipe
    ADD COLUMN IF NOT EXISTS speed_inputs jsonb NOT NULL DEFAULT '[]';

ALTER TABLE item_recipe
    DROP CONSTRAINT IF EXISTS item_recipe_speed_inputs_array;

ALTER TABLE item_recipe
    ADD CONSTRAINT item_recipe_speed_inputs_array
    CHECK (jsonb_typeof(speed_inputs) = 'array');

-- Author the shovel: a bar of iron halves forge time, MOVING iron off the interim
-- LLM-442 boost_inputs [{iron,1,+1}] output boost onto a speed input. The iron
-- dependency was relocated off nails onto shovels; LLM-511 reshapes it from
-- +output to +rate, the truer model of forging a shovel from a bar. Live shovel
-- base is OutputQty 1, 1 per 4h — with a bar in hand the cycle runs 2h. Ezekiel
-- keeps his `buy iron` restock (cap 6) as the iron source. The boost_inputs
-- rebuild strips ONLY the iron entry (filter, not a blanket clear) so any other
-- boost that exists is preserved (code_review). Corrective UPDATE: a DB with no
-- shovel recipe simply matches nothing (the DO block below tolerates an absent
-- row, the LLM-474 pattern).
UPDATE item_recipe
   SET speed_inputs = '[{"item": "iron", "qty": 1, "rate_pct": 200}]'::jsonb,
       boost_inputs = COALESCE(
           (SELECT jsonb_agg(elem)
              FROM jsonb_array_elements(boost_inputs) AS elem
             WHERE elem->>'item' IS DISTINCT FROM 'iron'),
           '[]'::jsonb)
 WHERE output_item = 'shovel';

-- Loud validate, LLM-474 pattern: if the shovel recipe IS present it must have
-- taken the iron speed input and no longer carry an iron boost (any non-iron
-- boost is left intact and NOT asserted on). Relative to what exists rather than
-- an absolute count so the integration-test template DB (schema + migrations, no
-- seed catalog) passes on an empty item_recipe. Catches a present row the UPDATE
-- failed to modify; does NOT catch an absent/renamed shovel (present shrinks with
-- authored together) — the backstop there is the perception speed cue
-- disappearing for the smith, not this block.
DO $$
DECLARE
    present  int;
    authored int;
BEGIN
    SELECT count(*) FILTER (WHERE true),
           count(*) FILTER (WHERE speed_inputs @> '[{"item": "iron"}]'::jsonb
                              AND NOT (boost_inputs @> '[{"item": "iron"}]'::jsonb))
      INTO present, authored
      FROM item_recipe
     WHERE output_item = 'shovel';
    IF authored <> present THEN
        RAISE EXCEPTION 'LLM-511: shovel present but failed to take iron speed_inputs / drop interim iron boost_inputs';
    END IF;
END $$;

COMMIT;
