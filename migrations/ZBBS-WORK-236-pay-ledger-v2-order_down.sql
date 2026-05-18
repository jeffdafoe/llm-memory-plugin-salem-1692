-- ZBBS-WORK-236 down: restore pre-v2 pay_ledger schema.
--
-- WARNING: rollback assumes every buyer_id, seller_id, and
-- consumer_actor_ids entry in pay_ledger is a valid UUID. If v2 has
-- written non-UUID ActorIDs (visitors: "vstr-<hex>", PCs with non-UUID
-- login names), the cast to UUID will fail. Operator must purge or
-- filter those rows before running this down.

BEGIN;

DROP INDEX IF EXISTS ix_pay_ledger_v2_in_flight;

ALTER TABLE pay_ledger DROP CONSTRAINT pay_ledger_fulfillment_status_check;
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
