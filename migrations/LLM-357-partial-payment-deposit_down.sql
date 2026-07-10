-- LLM-357 down: drop the partial-payment deposit column.
--
-- Dropping the column discards any in-flight deposit obligations: a partially
-- paid commission would be read as full-prepay on the next load (the buyer's
-- already-paid deposit is forgotten and offered_amount becomes the recorded
-- amount). Acceptable for a down migration — no environment should roll this
-- back with live partial orders outstanding.

BEGIN;

ALTER TABLE pay_ledger DROP CONSTRAINT IF EXISTS pay_ledger_deposit_amount_check;

ALTER TABLE pay_ledger DROP COLUMN deposit_amount;

COMMIT;
