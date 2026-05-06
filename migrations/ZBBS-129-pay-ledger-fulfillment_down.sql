-- ZBBS-129 down: revert pay-ledger fulfillment.

BEGIN;

ALTER TABLE item_kind
    DROP COLUMN hours_per_unit;

DROP INDEX IF EXISTS ix_pay_ledger_outstanding;

ALTER TABLE pay_ledger
    DROP CONSTRAINT IF EXISTS pay_ledger_fulfillment_status_check,
    DROP COLUMN fulfillment_status,
    DROP COLUMN delivered_on,
    DROP COLUMN ready_by;

COMMIT;
