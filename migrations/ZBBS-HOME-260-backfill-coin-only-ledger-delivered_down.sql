-- ZBBS-HOME-260 — revert backfill of coin-only pay_ledger rows.
--
-- Reverts fulfillment_status from 'delivered' back to 'ready' for
-- accepted coin-only rows (item_kind IS NULL). delivered_on is left
-- as-is rather than NULLed — it is set by other paths too (real
-- deliver_order completions for non-coin-only rows do not match this
-- WHERE clause), and we cannot reliably distinguish backfill values
-- from legitimate ones after the fact. Acceptable: the CHECK on
-- pay_ledger doesn't tie delivered_on to fulfillment_status.

BEGIN;

UPDATE pay_ledger
   SET fulfillment_status = 'ready'
 WHERE item_kind IS NULL
   AND fulfillment_status = 'delivered'
   AND state = 'accepted';

COMMIT;
