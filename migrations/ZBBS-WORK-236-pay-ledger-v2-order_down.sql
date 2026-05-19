-- ZBBS-WORK-236 down: restore pre-v2 pay_ledger schema.
--
-- WARNING — TWO operator prerequisites before running this rollback:
--
--   1. No rows with fulfillment_status='expired'. The old CHECK
--      constraint doesn't accept 'expired', so the ADD CONSTRAINT
--      below fails if any rows still have that status. The operator
--      must either DELETE those rows or UPDATE them to a v1-valid
--      status ('delivered' is the closest semantic equivalent for
--      a terminal Expired order, though it loses audit information).
--
--   2. No rows with non-UUID buyer_id, seller_id, or consumer
--      actor IDs. v2 ActorIDs are heterogeneous strings (visitors:
--      "vstr-<hex>", PCs with non-UUID login names) and the cast
--      back to UUID fails on them.
--
-- A pre-rollback check the operator can run:
--
--   SELECT COUNT(*) FILTER (WHERE fulfillment_status = 'expired') AS expired_rows,
--          COUNT(*) FILTER (WHERE buyer_id  !~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$') AS bad_buyers,
--          COUNT(*) FILTER (WHERE seller_id !~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$') AS bad_sellers
--   FROM pay_ledger;
--
-- All three must be 0 for this migration to succeed.

BEGIN;

DROP INDEX IF EXISTS ix_pay_ledger_v2_in_flight;

ALTER TABLE pay_ledger DROP CONSTRAINT IF EXISTS pay_ledger_fulfillment_status_check;
ALTER TABLE pay_ledger ADD CONSTRAINT pay_ledger_fulfillment_status_check
    CHECK (fulfillment_status IN ('pending', 'ready', 'delivered'));

ALTER TABLE pay_ledger DROP COLUMN expires_at;

ALTER TABLE pay_ledger ALTER COLUMN consumer_actor_ids TYPE UUID[] USING consumer_actor_ids::uuid[];
ALTER TABLE pay_ledger ALTER COLUMN seller_id TYPE UUID USING seller_id::uuid;
ALTER TABLE pay_ledger ALTER COLUMN buyer_id  TYPE UUID USING buyer_id::uuid;

ALTER TABLE pay_ledger ADD CONSTRAINT pay_ledger_buyer_id_fkey
    FOREIGN KEY (buyer_id) REFERENCES actor(id) ON DELETE CASCADE;
ALTER TABLE pay_ledger ADD CONSTRAINT pay_ledger_seller_id_fkey
    FOREIGN KEY (seller_id) REFERENCES actor(id) ON DELETE CASCADE;

COMMIT;
