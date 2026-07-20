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
-- copy and the Go side reuses the existing decode.
--
-- WHAT NULL MEANS, PRECISELY. NULL is "no goods leg recorded on this row" — which
-- is NOT the same claim as "this settlement was pure coin" (code_review). They
-- coincide for every row our code writes, because payItemsJSON writes the legs
-- whenever they exist. They diverge for history: a row whose audit entry is
-- missing, predates goods legs being persisted at all (pre-LLM-105), or carries a
-- malformed payload backfills to NULL and will therefore seed a coin rate.
--
-- That is a deliberate, conservative choice, not an oversight. The alternative —
-- treating "unknown" as "possibly barter" and excluding it — would silently drop a
-- large slice of genuine coin history and leave the book far thinner than the
-- defect warrants (mixed settlements are ~1.5% of the corpus). If we ever need the
-- stronger guarantee that no potentially-mixed settlement can seed, NULL cannot
-- provide it; that needs an explicit known/unknown status column. Pinned by
-- TestOrdersRepo_Integration_NullPayItemsSeedsAsPureCoin.
--
-- The CHECK constraint makes "NULL or a jsonb array" a permanent structural
-- invariant rather than a one-time assertion. That matters beyond tidiness: the
-- seed predicate calls jsonb_array_length on this column, which RAISES on a
-- non-array, so without the constraint a single bad hand-written row would break
-- boot seeding rather than merely being ignored.
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

-- Backfill from the settlement audit beat.
--
-- agent_action_log has no ledger_id COLUMN — it lives inside the jsonb payload,
-- alongside pay_items (the same payload /umbilical/settlements reads). Promoting
-- it to a real typed column is LLM-494; until then every reader hand-rolls this
-- extraction, and it needs two guards the obvious form does not have:
--
--   1. The regex alone does NOT make the ::bigint cast safe (code_review). A
--      100-digit value matches ^[0-9]+$ and then overflows, aborting the whole
--      migration. The length bound fixes that: bigint's maximum is 19 digits, so
--      anything at 18 or fewer is always in range. A genuine id will never come
--      close (they come from a sequence), so bounding at 18 costs nothing real and
--      avoids the fiddly lexicographic comparison the 19-digit boundary needs.
--
--   2. UPDATE ... FROM with a join that matches MULTIPLE source rows picks one
--      ARBITRARILY — Postgres does not define which (code_review). If two `paid`
--      rows ever carry the same ledger_id we would silently backfill whichever the
--      planner reached first. DISTINCT ON makes the choice explicit and stable:
--      highest audit-log id wins, i.e. the most recently written row for that
--      ledger. The DO block below additionally FAILS the migration if any ledger
--      has conflicting pay_items across rows, so "arbitrary but deterministic"
--      never quietly becomes "arbitrary and wrong".
--
-- Only rows that actually carry goods are written. A pure-coin settlement is left
-- NULL rather than set to '[]' — see the header on what NULL means.
WITH legs AS (
    SELECT DISTINCT ON (ledger_id) ledger_id, pay_items
      FROM (
            SELECT (al.payload ->> 'ledger_id')::bigint AS ledger_id,
                   al.payload -> 'pay_items'            AS pay_items,
                   al.id                                AS audit_id
              FROM agent_action_log al
             WHERE al.action_type = 'paid'
               AND al.payload ->> 'ledger_id' ~ '^[0-9]+$'
               AND length(al.payload ->> 'ledger_id') <= 18
               AND jsonb_typeof(al.payload -> 'pay_items') = 'array'
               AND jsonb_array_length(al.payload -> 'pay_items') > 0
           ) c
     ORDER BY ledger_id, audit_id DESC
)
UPDATE pay_ledger pl
   SET pay_items = legs.pay_items
  FROM legs
 WHERE legs.ledger_id = pl.id
   AND pl.pay_items IS NULL;

-- Guard 1: no ledger may have CONFLICTING goods legs across audit rows. If it
-- does, DISTINCT ON above silently picked one and the backfill is a guess. Fail
-- instead, loudly, with the count.
DO $$
DECLARE
    conflicting int;
BEGIN
    SELECT count(*) INTO conflicting
      FROM (
            SELECT (al.payload ->> 'ledger_id')::bigint AS ledger_id
              FROM agent_action_log al
             WHERE al.action_type = 'paid'
               AND al.payload ->> 'ledger_id' ~ '^[0-9]+$'
               AND length(al.payload ->> 'ledger_id') <= 18
               AND jsonb_typeof(al.payload -> 'pay_items') = 'array'
               AND jsonb_array_length(al.payload -> 'pay_items') > 0
             GROUP BY 1
            HAVING count(DISTINCT al.payload -> 'pay_items') > 1
           ) d;
    IF conflicting > 0 THEN
        RAISE EXCEPTION 'LLM-493: % ledger id(s) have conflicting pay_items across audit rows — backfill would be a guess', conflicting;
    END IF;
END $$;

-- Guard 2: every backfilled value must be a non-empty jsonb ARRAY.
--
-- The predicate uses CASE, not `jsonb_typeof(...) <> 'array' OR jsonb_array_length(...)`.
-- Postgres does NOT guarantee short-circuit evaluation of boolean operators, so
-- the OR form can evaluate jsonb_array_length on an object or scalar and raise
-- "cannot get array length of a non-array" — aborting with a confusing internal
-- error instead of this block's actionable one (code_review). CASE has defined
-- evaluation order and cannot.
DO $$
DECLARE
    malformed int;
BEGIN
    SELECT count(*) INTO malformed
      FROM pay_ledger
     WHERE pay_items IS NOT NULL
       AND CASE jsonb_typeof(pay_items)
             WHEN 'array' THEN jsonb_array_length(pay_items) = 0
             ELSE true
           END;
    IF malformed > 0 THEN
        RAISE EXCEPTION 'LLM-493: % pay_ledger row(s) have a malformed pay_items value', malformed;
    END IF;
END $$;

-- Make "NULL or a jsonb array" permanent rather than a one-time assertion. The
-- seed predicate calls jsonb_array_length on this column and that RAISES on a
-- non-array, so without this a single bad hand-written row would break boot
-- seeding for the whole village rather than merely being skipped. NOT VALID is
-- deliberately not used — the guards above have just proven every existing row
-- conforms, so the validating scan is cheap and worth doing now.
ALTER TABLE pay_ledger
    DROP CONSTRAINT IF EXISTS pay_ledger_pay_items_is_array;
ALTER TABLE pay_ledger
    ADD CONSTRAINT pay_ledger_pay_items_is_array
    CHECK (pay_items IS NULL OR jsonb_typeof(pay_items) = 'array');

COMMIT;
