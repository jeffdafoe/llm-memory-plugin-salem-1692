-- LLM-493: persist the goods legs of a settlement on pay_ledger, so the coin
-- price book can exclude barter consistently at BOTH ingestion paths.
--
-- The problem. The price book is a per-(seller, item) record of observed coin
-- rates. A pure goods-for-goods barter is deliberately excluded: it settles at
-- offered_amount = 0, and recording it would enter a "free" reading that poisons
-- every rate derived from that key (ZBBS-HOME-393). Correct.
--
-- But a MIXED coin+goods settlement passes that guard, because offered_amount is
-- positive, and is then recorded with only its coin leg against the FULL quantity.
-- Live example (2026-07-14): Josiah Thorne bought 5 nails from Ezekiel Crane for
-- 2 coins PLUS 2 skillets and 2 wheat. The book stores 5 units for 2 coins and
-- concludes nails go for 0.4 coins each. That is worse than the pure-barter case:
-- pure barter leaves a gap, mixed leaves a wrong number, and wrong numbers
-- propagate into every buy anchor and margin verdict computed off that key.
--
-- Why a column is needed rather than a one-line guard. The live subscriber
-- (engine/sim/cascade/price_book.go) has the goods legs on the resolved event and
-- could filter them today. Boot seeding cannot: loadRecentPricesSQL selects from
-- pay_ledger, and pay_ledger has offered_amount and NO goods column at all — the
-- legs are persisted only inside agent_action_log's jsonb payload. LLM-285
-- established that the two ingestion paths must agree, precisely so a restart
-- cannot re-import rows the live subscriber declined. Tightening only the
-- subscriber would produce the worst possible shape: a settlement excluded while
-- the engine runs, then silently re-imported at its false rate on the next boot.
-- The village redeploys several times a day, so that divergence would be constant.
--
-- Why the full legs and not a boolean. A `paid_with_goods` flag would satisfy the
-- guard at identical migration cost, while foreclosing the only non-synthetic way
-- to ever account for barter — an in-kind exchange record ("wheat traded for
-- flour, about one for one"), which needs the actual items and quantities. Storing
-- the legs commits us to nothing and avoids a second migration if that is built.
-- Deriving a coin VALUE for the goods leg is explicitly rejected (LLM-492): it is
-- circular, and it manufactures certainty the data does not contain.
--
-- Shape. jsonb array of {"item": <kind>, "qty": <n>}, matching the shape already
-- written into agent_action_log's payload.pay_items, so the backfill is a direct
-- copy and the Go side reuses the existing decode. NULL means "no goods leg
-- recorded" and reads as pure coin — which is correct for every historical row
-- that genuinely was pure coin, and is the safe default for the pre-LLM-105 rows
-- that predate goods legs being recorded at all.
--
-- ENGINE-OWNED TABLE. pay_ledger is written by the running engine. Apply with the
-- engine STOPPED (stop -> migrate -> start, the standard deploy order). The
-- ADD COLUMN is safe regardless, and the upsert's ON CONFLICT clause does not
-- touch pay_items, so a running engine cannot clobber a backfilled value — but
-- keep the standard order anyway.
--
-- Rerun-safe: ADD COLUMN IF NOT EXISTS, and the backfill's `pl.pay_items IS NULL`
-- predicate makes a second run a no-op.

BEGIN;

ALTER TABLE pay_ledger
    ADD COLUMN IF NOT EXISTS pay_items jsonb;

-- Backfill from the settlement audit beat. agent_action_log has no ledger_id
-- COLUMN — it lives inside the jsonb payload, alongside pay_items (this is the
-- same payload /umbilical/settlements reads). The regex guard on ledger_id keeps
-- the ::bigint cast from erroring on any row whose payload carries a
-- non-numeric or absent id; those simply do not match and stay NULL.
--
-- Only rows that actually carry goods are written. A pure-coin settlement is left
-- NULL rather than set to '[]', so NULL keeps one unambiguous meaning ("no goods
-- leg on this row") instead of splitting into "none recorded" vs "recorded empty".
UPDATE pay_ledger pl
   SET pay_items = al.payload -> 'pay_items'
  FROM agent_action_log al
 WHERE al.action_type = 'paid'
   AND al.payload ->> 'ledger_id' ~ '^[0-9]+$'
   AND (al.payload ->> 'ledger_id')::bigint = pl.id
   AND jsonb_typeof(al.payload -> 'pay_items') = 'array'
   AND jsonb_array_length(al.payload -> 'pay_items') > 0
   AND pl.pay_items IS NULL;

-- Guard: every backfilled value must be a non-empty jsonb ARRAY. A scalar or
-- object here would mean the audit payload shape drifted from what the Go decode
-- expects, and would silently break the seed predicate rather than fail loudly.
DO $$
DECLARE
    malformed int;
BEGIN
    SELECT count(*) INTO malformed
      FROM pay_ledger
     WHERE pay_items IS NOT NULL
       AND (jsonb_typeof(pay_items) <> 'array' OR jsonb_array_length(pay_items) = 0);
    IF malformed > 0 THEN
        RAISE EXCEPTION 'LLM-493: % pay_ledger row(s) have a malformed pay_items value', malformed;
    END IF;
END $$;

COMMIT;
