-- ZBBS-WORK-236: pay_ledger as v2 Order durable home.
--
-- v2 engine rewrite collapses Order onto pay_ledger — pay_ledger IS the
-- durable Order record. World.Orders is an in-memory cache of in-flight
-- (Ready/Pending) rows; pg holds the full history including terminal
-- states. This migration adapts pay_ledger to v2's needs:
--
--   1. buyer_id, seller_id, consumer_actor_ids → TEXT (was UUID).
--      v2 ActorIDs are heterogeneous strings (visitors: "vstr-<hex>",
--      persistent NPCs: UUID-shaped, PCs: login-username derived).
--      The uuid type + FK to actor(id) blocks v2's design. FKs dropped.
--
--   2. expires_at column added — TTL for Ready orders. v1 had ready_by
--      (DATE, when goods are due) but no auto-expire. v2 adds Expired
--      as a terminal safety-net state; expires_at is its trigger field.
--      NULL on legacy rows is fine (the v2 TTL sweep only operates on
--      rows it itself wrote).
--
--   3. fulfillment_status CHECK extended to include 'expired'.
--
--   4. Partial index for v2 LoadAll's hot path:
--         WHERE state = 'accepted'
--           AND fulfillment_status IN ('ready', 'pending')
--      Covers both Ready (current MVP) and Pending (deferred craft
--      lifecycle — items with hours_per_unit > 0 will start Pending and
--      flip Ready via mark_order_ready(); ZBBS-129 work-log documents
--      the design). Today v2 substrate only emits Ready; the index is
--      forward-compatible.
--
-- v2 writes to pay_ledger always carry state='accepted' (the v1 haggle
-- chain — pending/declined/countered/withdrawn/failed — happens
-- in-memory before any Order exists). NULL huddle_id, scene_id,
-- quoted_unit_amount, message, counter_amount, parent_id on v2-written
-- rows. v1 readers already tolerate NULL huddle_id (pre-MEM-121 rows).
--
-- Rollback caveat: the _down.sql restores UUID typing. If v2 has
-- written non-UUID ActorIDs by rollback time (visitors, PCs with
-- non-UUID login names), the cast back to UUID will fail. Operator
-- must drain or filter those rows before rolling back.

BEGIN;

-- Drop the FKs and convert ID columns to TEXT.
-- IF EXISTS guards against re-runs and against operator environments
-- where ZBBS-128's constraint naming differs (the FK names below
-- match the default Postgres naming convention, but defensive is
-- cheaper than a partial-applied migration).
ALTER TABLE pay_ledger DROP CONSTRAINT IF EXISTS pay_ledger_buyer_id_fkey;
ALTER TABLE pay_ledger DROP CONSTRAINT IF EXISTS pay_ledger_seller_id_fkey;
ALTER TABLE pay_ledger ALTER COLUMN buyer_id  TYPE TEXT USING buyer_id::text;
ALTER TABLE pay_ledger ALTER COLUMN seller_id TYPE TEXT USING seller_id::text;
ALTER TABLE pay_ledger ALTER COLUMN consumer_actor_ids TYPE TEXT[] USING consumer_actor_ids::text[];

-- New TTL column for v2 Expired safety-net state.
ALTER TABLE pay_ledger ADD COLUMN expires_at TIMESTAMP WITH TIME ZONE;

-- Extend fulfillment_status CHECK with 'expired'.
ALTER TABLE pay_ledger DROP CONSTRAINT IF EXISTS pay_ledger_fulfillment_status_check;
ALTER TABLE pay_ledger ADD CONSTRAINT pay_ledger_fulfillment_status_check
    CHECK (fulfillment_status IN ('pending', 'ready', 'delivered', 'expired'));

-- LoadAll hot-path index — covers Ready (today) and Pending (future
-- craft lifecycle) with one definition.
CREATE INDEX ix_pay_ledger_v2_in_flight
    ON pay_ledger (id)
    WHERE state = 'accepted'
      AND fulfillment_status IN ('ready', 'pending');

COMMIT;
