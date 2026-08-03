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

-- Same completeness guard the up migration carries, with the down values. An
-- UPDATE matching nothing is a success in Postgres, so without this a
-- partially-populated catalog would roll back "successfully" while leaving
-- garment kinds missing or still on the new budgets. The up migration now
-- requires all five rows; a rollback that quietly restored fewer would leave
-- the catalog in a state neither migration describes.
DO $$
DECLARE
    wrong INTEGER;
BEGIN
    SELECT count(*) INTO wrong
      FROM (VALUES
                ('shift',    360),
                ('breeches', 480),
                ('gown',     480),
                ('cloak',    600),
                ('coat',     600)
           ) AS v(name, wear_minutes)
      LEFT JOIN public.item_kind k ON k.name = v.name
     WHERE k.name IS NULL
        OR k.wear_minutes <> v.wear_minutes;

    IF wrong > 0 THEN
        RAISE EXCEPTION 'LLM-589 down: % garment kind(s) missing from item_kind or not restored to the LLM-422 wear budget', wrong;
    END IF;
END $$;

COMMIT;
