-- ZBBS-HOME-260 — backfill coin-only pay_ledger rows to 'delivered'.
--
-- Companion to the engine fix in pay_ledger.go: insertPayLedgerPending
-- now sets fulfillment_status = 'delivered' for item_kind IS NULL
-- (coin-only transfers — tips, gifts, condolences, news payments).
-- Coins move atomically inside the transfer tx, so there is nothing
-- left for executeDeliverOrder to ship.
--
-- Pre-fix rows were inserted with fulfillment_status = 'ready' and
-- then surfaced in the seller's "outstanding orders" perception via
-- readyOrdersForSeller (engine/order_fulfillment.go:735, which filters
-- pl.fulfillment_status = 'ready'). The seller's LLM keeps calling
-- deliver_order(N), the engine keeps rejecting "ledger row N carries
-- no item to deliver", and the row never clears.
--
-- Observed in prod 2026-05-11: Hannah Boggs hit this 9 times in 24h
-- on rows 101, 106, 107, 109. All four are coin-only (item_kind NULL,
-- offered_amount 4-26) handed to her by Nathaniel Pratt the wool-buyer
-- (1a1f5155-...) and Ezekiel Crane (019da6f9-...) as tips/gifts.
--
-- Filter to state='accepted' so we don't relabel declined / withdrawn
-- coin-only rows: those never transferred coins, so 'delivered' would
-- be misleading audit data. delivered_on falls back to resolved_at
-- (which executePay stamps when it flips state to 'accepted') or NOW()
-- as a last resort.

BEGIN;

UPDATE pay_ledger
   SET fulfillment_status = 'delivered',
       delivered_on = COALESCE(delivered_on, resolved_at, NOW())
 WHERE item_kind IS NULL
   AND fulfillment_status = 'ready'
   AND state = 'accepted';

COMMIT;
