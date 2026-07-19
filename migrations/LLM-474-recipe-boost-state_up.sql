-- LLM-474: state-keyed booster inputs on recipes, and the first consumer —
-- a lit hearth makes a better batch.
--
-- boost_state is a JSONB array of {state, bonus_qty} objects — the world-state
-- mirror of boost_inputs. Where a boost_input is earned by HOLDING an item and
-- consumes it, a boost_state is earned by a condition being true when the batch
-- lands and consumes NOTHING: its cost was paid upstream. For `hearth_lit` that
-- upstream cost is the firewood an earlier stoke burned into the fire, so this
-- rewards keeping the fire in rather than opening a second sink for the wood.
--
-- Same posture as `inputs` / `boost_inputs`: engine-validated Go-side (known
-- state name, positive bonus_qty, no duplicate state); the DB enforces only the
-- array shape.
--
-- CRITICAL INVARIANT: a boost_state is strictly additive and must NEVER gate.
-- Every dish below stays producible at its full base OutputQty with every
-- hearth in the village stone cold. Food is the survival good — see LLM-444's
-- never-gate-food rule and the deadlock reasoning on LLM-474.

BEGIN;

ALTER TABLE item_recipe
    ADD COLUMN IF NOT EXISTS boost_state jsonb NOT NULL DEFAULT '[]';

ALTER TABLE item_recipe
    DROP CONSTRAINT IF EXISTS item_recipe_boost_state_array;

ALTER TABLE item_recipe
    ADD CONSTRAINT item_recipe_boost_state_array
    CHECK (jsonb_typeof(boost_state) = 'array');

-- The fire-cooked dishes. Confirmed against live state 2026-07-19: every one of
-- these is produced only at a hearth-bearing structure (porridge / fried_meat /
-- journeycake at the Inn by Hannah Boggs; bread / journeycake at the Tavern by
-- John Ellis), so each producer can actually earn its bonus.
--
-- Deliberately NOT included: cheese (curdled and pressed, not cooked — and
-- produced at the hearthless Ellis Farm) and ale (brewed on its own vessel).
--
-- Bonus sizing against a 2-coin stick of firewood that buys 180 minutes of burn
-- across several batches. fried_meat is the one to watch: its base batch is 1,
-- so the integer floor of +1 is proportionally the largest bonus here. If it
-- reads too strong in play the lever is its OutputQty, not this row — there is
-- no bonus smaller than 1.
UPDATE item_recipe SET boost_state = '[{"state": "hearth_lit", "bonus_qty": 3}]'::jsonb
 WHERE output_item = 'porridge';
UPDATE item_recipe SET boost_state = '[{"state": "hearth_lit", "bonus_qty": 2}]'::jsonb
 WHERE output_item = 'journeycake';
UPDATE item_recipe SET boost_state = '[{"state": "hearth_lit", "bonus_qty": 2}]'::jsonb
 WHERE output_item = 'bread';
UPDATE item_recipe SET boost_state = '[{"state": "hearth_lit", "bonus_qty": 1}]'::jsonb
 WHERE output_item = 'fried_meat';

-- Loud validate: every fire-cooked recipe that IS present must have taken its
-- boost. Deliberately relative to what exists rather than a hard count of 4,
-- because the integration-test template database is built from schema +
-- migrations with no seed catalog and an absolute assertion fails there on a
-- legitimately empty table.
--
-- Know what this does and does not catch. It CATCHES a present row that the
-- UPDATE failed to modify (a botched WHERE, a jsonb write silently rejected).
-- It does NOT catch a MISSING row — a renamed or absent output_item shrinks
-- `present` and `authored` together and passes clean. Guarding that would need
-- an absolute count, which the empty template cannot satisfy. If a fire-cooked
-- dish is ever renamed, this migration goes quiet rather than loud; the backstop
-- is the perception cue disappearing for its cook, not this block.
DO $$
DECLARE
    present  int;
    authored int;
BEGIN
    SELECT count(*) FILTER (WHERE true),
           count(*) FILTER (WHERE boost_state @> '[{"state": "hearth_lit"}]'::jsonb)
      INTO present, authored
      FROM item_recipe
     WHERE output_item IN ('porridge', 'journeycake', 'bread', 'fried_meat');
    IF authored <> present THEN
        RAISE EXCEPTION 'LLM-474: % of % present fire-cooked recipes failed to take hearth_lit',
            present - authored, present;
    END IF;
END $$;

COMMIT;
