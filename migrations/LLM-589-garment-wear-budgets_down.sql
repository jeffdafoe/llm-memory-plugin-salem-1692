-- LLM-589 down: restore the LLM-422 authored garment wear budgets.
--
-- Reverting puts a shift back to 360 worked minutes — spent inside one working
-- day.
--
-- This restores the authored values unconditionally, overwriting any live
-- operator retune of these five kinds. That is deliberate and matches the
-- repository's convention: a down migration returns the prior authored state,
-- so preserving an intervening manual edit would make rollback
-- non-deterministic.
--
-- A live Actor.GarmentWear counter left above a restored budget is not touched
-- here and does not need to be — applyGarmentWear clamps a counter that exceeds
-- the catalog budget at next use (engine/sim/garment_wear.go:167-170), which is
-- the same path an operator retuning the value down live already takes.

BEGIN;

UPDATE public.item_kind AS k
   SET wear_minutes = v.wear_minutes
  FROM (VALUES
            ('shift',    360),
            ('breeches', 480),
            ('gown',     480),
            ('cloak',    600),
            ('coat',     600)
       ) AS v(name, wear_minutes)
 WHERE k.name = v.name;

COMMIT;
