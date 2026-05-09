-- ZBBS-WORK-204 (commit 2 of 2) down migration.
--
-- Best-effort reversal: drops the vendor_flavor column and the
-- engine-auto seed rows. Cannot reliably restore the prior
-- home_structure_id values — those were operator-assigned (admin
-- console / earlier migrations) and we don't carry an audit table to
-- replay from. If the deploy needs to roll back AND the prior home
-- assignments must be restored, the operator runs targeted UPDATEs
-- by hand from a backup.

BEGIN;

-- 1. Drop the partial unique index that gates auto-rebook
-- idempotency. Engine code post-rollback won't reference this
-- constraint because it's also being rolled back; safe to drop.
DROP INDEX IF EXISTS pay_ledger_lodging_active_once;

-- 2. Remove the seed nights_stay rows we inserted. Keyed off the
-- distinctive message string so admin/LLM-driven nights_stay rows
-- created after the up-migration ran are preserved.
DELETE FROM pay_ledger
 WHERE item_kind = 'nights_stay'
   AND message = 'ZBBS-WORK-204 starter';

-- 3. Drop the vendor_flavor column. Hannah's flavor string is lost;
-- the engine's perception block falls back to no-flavor. Reapply
-- post-rollback if needed.
ALTER TABLE actor
    DROP COLUMN IF EXISTS vendor_flavor;

COMMIT;
