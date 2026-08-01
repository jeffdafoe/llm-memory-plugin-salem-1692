-- LLM-589 down: restore the LLM-422 authored garment wear budgets.
--
-- Reverting puts a shift back to 360 worked minutes — spent inside one working
-- day. Note that any live Actor.GarmentWear entry sitting above the restored
-- budget is not corrected here; the LLM-422 clamp reduces it at next use, which
-- is the same path an operator retuning the value down live would take.

BEGIN;

UPDATE public.item_kind
   SET wear_minutes = CASE name
                          WHEN 'shift'    THEN 360
                          WHEN 'breeches' THEN 480
                          WHEN 'gown'     THEN 480
                          WHEN 'cloak'    THEN 600
                          WHEN 'coat'     THEN 600
                      END
 WHERE name IN ('shift', 'breeches', 'gown', 'cloak', 'coat');

COMMIT;
