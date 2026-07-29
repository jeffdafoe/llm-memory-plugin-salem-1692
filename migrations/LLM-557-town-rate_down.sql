-- LLM-557 down: drop the town-rate arrears column.
--
-- Destroys any outstanding arrears, which is correct for a rollback: the pre-LLM-557
-- binary has no concept of the levy, so a balance it cannot read or settle is dead
-- weight. Re-applying the up migration starts every business from zero owed, and the
-- daily assessment re-accrues from there.
--
-- Guarded by IF EXISTS so a down against a database that never got the column is a
-- no-op rather than an error.

BEGIN;

ALTER TABLE village_object DROP COLUMN IF EXISTS rate_owed;

COMMIT;
