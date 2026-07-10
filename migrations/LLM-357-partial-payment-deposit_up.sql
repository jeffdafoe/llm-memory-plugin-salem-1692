-- LLM-357: partial-payment commissions — a buyer can put a deposit down on a
-- made-to-order good and settle the balance at pickup.
--
-- pay_ledger.deposit_amount: the coins actually charged to the buyer at accept
-- when the order is a partial-payment commission. The full agreed price stays in
-- offered_amount; the balance the seller collects at deliver_order is
-- offered_amount - deposit_amount. NULL (the default, and every pre-357 row) OR
-- 0 means "full prepay" — the whole offered_amount was taken at accept. The
-- engine reads it through orderAmountPaidAtAccept / orderBalanceDue, both of
-- which treat NULL/0 and any deposit >= offered_amount as full prepay, so a
-- legacy NULL row, a v2 full-prepay 0 row, and a full-prepay commission all
-- behave identically.
--
-- Nullable + no default: adding a nullable column is a metadata-only change (no
-- table rewrite), and NULL is exactly the "full prepay" sentinel the engine
-- already handles, so existing rows need no backfill.

BEGIN;

ALTER TABLE pay_ledger ADD COLUMN deposit_amount INTEGER;

-- A deposit is a coin figure: never negative, and never more than the total
-- price (offered_amount) it is a fraction of. Guard the invariant against
-- out-of-band writes (mirrors the offered_amount / quoted_unit_amount CHECKs on
-- this table).
ALTER TABLE pay_ledger
    ADD CONSTRAINT pay_ledger_deposit_amount_check
    CHECK (deposit_amount IS NULL OR (deposit_amount >= 0 AND deposit_amount <= offered_amount));

COMMENT ON COLUMN pay_ledger.deposit_amount IS
    'LLM-357: coins charged at accept on a partial-payment commission; balance (offered_amount - deposit_amount) settles at deliver_order. NULL or 0 = full prepay.';

COMMIT;
