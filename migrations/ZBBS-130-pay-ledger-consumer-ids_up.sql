-- ZBBS-130: pay_ledger consumer IDs (phase C persistence under v2 fulfillment).
--
-- Phase C of sales-and-gifts let a buyer fund an at-source group order:
-- "Jefferey buys 4 ales for himself + 3 friends." Pre-ZBBS-129 this
-- worked at pay-accept time — applyConsumption fired for each named
-- consumer atomically with the coin transfer.
--
-- ZBBS-129 step 2 splits delivery off pay-accept: items now ship at
-- deliver_order time, not at the moment of payment. With phase C, that
-- means we need to know who the consumers ARE at delivery time. The
-- pay tool's `consumers: [...]` argument is a request-scoped list of
-- display names; the names are resolved to actor IDs at pay-accept,
-- but the ledger row only carried buyer + seller. The named consumers
-- were never persisted.
--
-- This migration adds consumer_actor_ids (a small UUID array). NULL =
-- the legacy single-consumer flow (buyer is the implicit consumer);
-- non-empty = phase C, listing every named consumer as resolved at
-- pay-accept (case-insensitive name match → actor.id). At deliver_order
-- the engine reads the array, locks each row, applies consumption only
-- for consumers still co-located with the seller (matches the existing
-- room-locality rule). Consumers who left the room between pay-accept
-- and delivery don't get fed; their share is wasted, mirroring the
-- "you can't drink someone's ale from across the village" friction.

BEGIN;

ALTER TABLE pay_ledger
    ADD COLUMN consumer_actor_ids UUID[];

COMMIT;
