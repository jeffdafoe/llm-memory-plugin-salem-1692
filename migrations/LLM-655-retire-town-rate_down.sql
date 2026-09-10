-- LLM-655 rollback: restore the town-rate arrears column so an engine build that
-- still carries LLM-557 can load village_object.
--
-- The balances are not restored (they were forgiven by the up), and the two
-- setting rows are not re-inserted — the LLM-557 engine falls back to its compiled
-- defaults (1 coin a day, capped at 3), so accrual resumes from zero on the next
-- rotation. Engine-STOPPED like the up.

BEGIN;

ALTER TABLE village_object ADD COLUMN IF NOT EXISTS rate_owed integer NOT NULL DEFAULT 0;

COMMIT;
