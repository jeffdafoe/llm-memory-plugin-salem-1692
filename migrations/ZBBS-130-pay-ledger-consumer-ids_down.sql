-- ZBBS-130 down: drop pay_ledger.consumer_actor_ids.

BEGIN;

ALTER TABLE pay_ledger
    DROP COLUMN consumer_actor_ids;

COMMIT;
