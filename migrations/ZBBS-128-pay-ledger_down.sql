-- ZBBS-128 down: drop pay_ledger and its indexes.
--
-- Indexes go via DROP TABLE; explicit DROP INDEX commands aren't
-- needed (PG drops dependent objects in CASCADE-by-table). Listed
-- here as a comment for the migration runner's sanity check that the
-- forward and backward shapes match:
--   - ix_pay_ledger_scene_at
--   - ix_pay_ledger_buyer_seller
--   - ix_pay_ledger_pending

BEGIN;

DROP TABLE IF EXISTS pay_ledger;

COMMIT;
