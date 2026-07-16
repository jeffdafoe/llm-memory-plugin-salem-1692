-- LLM-422 rollback: drop the garment wear axis — the actor_inventory.worn_minutes_left
-- column and the item_kind.wear_minutes column (with its garment budgets). Manual-
-- rollback only (the migration runner never applies _down).
--
-- Column drops are unconditional IF EXISTS. No FK / view depends on either column
-- (both are plain scalar attributes), so the drops are clean. Dropping wear_minutes
-- takes the garment budgets with it; the garment item_kind rows themselves are
-- LLM-410's and are left intact.

BEGIN;

ALTER TABLE actor_inventory DROP COLUMN IF EXISTS worn_minutes_left;
ALTER TABLE item_kind DROP COLUMN IF EXISTS wear_minutes;

COMMIT;
