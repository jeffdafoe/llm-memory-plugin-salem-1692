-- ZBBS-129: pay-ledger fulfillment — order book, ready/delivered, capacity.
--
-- Extends the ZBBS-128 pay_ledger from a payment record into a service-
-- fulfillment record. Three new columns capture when an order is due,
-- when it was actually handed over, and where it currently sits in
-- the fulfillment lifecycle. Plus item_kind.hours_per_unit so the
-- engine can compute capacity ("Ezekiel has 14 hours of outstanding
-- work; daily shift is 8 hours; earliest unstarted slot is 2 days").
--
-- Why this generalizes the ledger:
--
--   1. Lodging needs a future check-in date. "Pay tonight, arrive in
--      3 days" doesn't fit a payment-only ledger; it needs ready_by
--      and fulfillment_status.
--   2. Crafts/services with lead time (blacksmith horseshoes, miller
--      flour orders) need the same shape — buyer pays now, vendor
--      delivers later.
--   3. Lateness becomes derivable: (CURRENT_DATE > ready_by) AND
--      (fulfillment_status != 'delivered'). No new lateness column,
--      just a query.
--
-- Status machine (orthogonal to the existing payment-state machine):
--
--      pending ──→ ready ──→ delivered
--
--   - Items with hours_per_unit > 0 (crafts) start at 'pending' and
--     flip to 'ready' when the vendor calls mark_order_ready().
--   - Items with hours_per_unit NULL/0 (food, drink, lodging) start
--     at 'ready' directly — no production phase.
--   - Every transaction passes through 'ready' and is finalized by an
--     explicit deliver_order() call. No item skips to 'delivered'.
--     Inventory transfer happens at deliver_order() time, not at pay-
--     accept (behavior change vs ZBBS-128 step 2).
--
-- Backfill rule for legacy ZBBS-128 rows:
--
--   - ready_by           = created_at::date
--                          (every v1 transaction was an immediate good
--                          due same-day; this is honest)
--   - fulfillment_status = 'delivered'
--                          (the column is orthogonal to state, but for
--                          declined/countered/withdrawn/failed rows it
--                          is meaningless — pick the inert terminal
--                          value rather than leave them perpetually
--                          'pending')
--   - delivered_on       = resolved_at when state='accepted', else NULL
--                          (delivered_on and resolved_at coincided in
--                          v1 since fulfillment was atomic with payment)
--
-- Design doc: shared/tasks/pay-ledger-fulfillment/design

BEGIN;

-- New columns nullable so the backfill UPDATE has somewhere to write.
ALTER TABLE pay_ledger
    ADD COLUMN ready_by           DATE,
    ADD COLUMN delivered_on       TIMESTAMP WITH TIME ZONE,
    ADD COLUMN fulfillment_status VARCHAR(16);

UPDATE pay_ledger
SET ready_by           = created_at::date,
    fulfillment_status = 'delivered',
    delivered_on       = CASE WHEN state = 'accepted' THEN resolved_at ELSE NULL END;

ALTER TABLE pay_ledger
    ALTER COLUMN ready_by           SET NOT NULL,
    ALTER COLUMN fulfillment_status SET NOT NULL,
    ADD CONSTRAINT pay_ledger_fulfillment_status_check
        CHECK (fulfillment_status IN ('pending','ready','delivered'));

-- Outstanding-orders / lateness reads. Vendor's check_order_book
-- sorts outstanding rows by ready_by ASC then created_at ASC, scoped
-- by seller_id. Lateness query (CURRENT_DATE > ready_by AND not yet
-- delivered) hits the same partial set. Partial keeps the index small
-- once most rows are terminal.
CREATE INDEX ix_pay_ledger_outstanding
    ON pay_ledger (seller_id, ready_by, created_at)
    WHERE state = 'accepted' AND fulfillment_status <> 'delivered';

-- Per-unit production hours. NULL or 0 means immediate (no production
-- phase). Positive value drives the capacity-planning headline that
-- gets injected into the deliberation prompt for new orders.
ALTER TABLE item_kind
    ADD COLUMN hours_per_unit SMALLINT
        CHECK (hours_per_unit IS NULL OR hours_per_unit >= 0);

COMMIT;
