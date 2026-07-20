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
-- predicate makes a second run a no-op. The CHECK constraint uses
-- DROP CONSTRAINT IF EXISTS + ADD, which is replacement semantics: a rerun
-- recreates the constraint from this definition, discarding any hand-modified
-- version carrying the same name (code_review). That is the intended behaviour —
-- this migration owns that constraint — but it is a deliberate choice, not an
-- accident, and a hand-tuned variant would be silently reverted by a rerun.

BEGIN;

ALTER TABLE pay_ledger
    ADD COLUMN IF NOT EXISTS pay_items jsonb;

-- Backfill from the settlement audit beat.
--
-- agent_action_log has no ledger_id COLUMN — it lives inside the jsonb payload,
-- alongside pay_items (the same payload /umbilical/settlements reads). Promoting
-- it to a real typed column is LLM-494; until then every reader hand-rolls this
-- extraction, and it needs care in two places the obvious form gets wrong.
--
-- 1. A WHERE PREDICATE DOES NOT PROTECT A CAST. Postgres does not guarantee that
--    WHERE conditions are evaluated before expressions in the select list — the
--    planner may evaluate the cast while producing rows for DISTINCT ON, or
--    reorder evaluation for any other reason (code_review). So filtering on
--    `~ '^[0-9]+$' AND length(...) <= 18` in a WHERE and casting in the SELECT is
--    NOT safe: a 19+ digit digits-only value can still raise bigint overflow and
--    abort the migration.
--
--    The guards therefore live INSIDE a CASE, which does have contractual
--    evaluation order, so the cast is only ever reached for a value already known
--    to be in range. Bigint's maximum is 19 digits, so 18 or fewer is always safe;
--    a real id comes from a sequence and will never approach it, which is why the
--    fiddly lexicographic comparison for the 19-digit boundary is not worth it.
--
--    Same trap as the boolean-OR one in guard 2 below: both are cases of assuming
--    an evaluation order Postgres explicitly declines to promise.
--
-- 2. UPDATE ... FROM matching MULTIPLE source rows picks one ARBITRARILY, and
--    Postgres does not define which (code_review). If two `paid` rows ever carry
--    the same ledger_id we would silently backfill whichever the planner reached
--    first. DISTINCT ON makes the choice explicit and stable — highest audit-log
--    id wins, i.e. the most recently written row for that ledger — and the guard
--    below FAILS the migration outright if any ledger has conflicting legs, so
--    "deterministic" can never quietly become "wrong".
--
-- The candidate set is materialised ONCE into a temp table rather than repeated as
-- a subquery in both the guard and the UPDATE. Two hand-maintained copies of the
-- same four predicates would be free to drift, and if they did the guard would
-- silently stop covering the statement it exists to protect.
CREATE TEMP TABLE llm493_settlement_legs ON COMMIT DROP AS
WITH candidates AS (
    SELECT CASE
               WHEN al.payload ->> 'ledger_id' ~ '^[0-9]+$'
                AND length(al.payload ->> 'ledger_id') <= 18
               THEN (al.payload ->> 'ledger_id')::bigint
           END                       AS ledger_id,
           al.payload -> 'pay_items' AS pay_items,
           al.id                     AS audit_id
      FROM agent_action_log al
     WHERE al.action_type = 'paid'
)
SELECT ledger_id, pay_items, audit_id
  FROM candidates
 WHERE ledger_id IS NOT NULL
   AND CASE jsonb_typeof(pay_items)
         WHEN 'array' THEN jsonb_array_length(pay_items) > 0
         ELSE false
       END;

-- Guard 1: no ledger may have CONFLICTING goods legs across audit rows. If one
-- does, the DISTINCT ON below would silently pick a winner and the backfill would
-- be a guess. Fail instead, loudly, with the count.
--
-- Runs BEFORE the UPDATE — a later RAISE would roll the update back just the same,
-- but failing first is clearer and does less work.
--
-- SCOPE, deliberately: this only sees rows that survived into the temp table, i.e.
-- valid ledger ids carrying non-empty arrays. That is exactly the set eligible for
-- backfill, so a malformed or empty duplicate is IGNORED rather than reported as a
-- conflict — correct, because such a row could never have been chosen anyway.
DO $$
DECLARE
    conflicting int;
BEGIN
    SELECT count(*) INTO conflicting
      FROM (
            SELECT ledger_id
              FROM llm493_settlement_legs
             GROUP BY ledger_id
            HAVING count(DISTINCT pay_items) > 1
           ) d;
    IF conflicting > 0 THEN
        RAISE EXCEPTION 'LLM-493: % ledger id(s) have conflicting pay_items across audit rows — backfill would be a guess', conflicting;
    END IF;
END $$;

-- Only rows that actually carry goods are written. A pure-coin settlement is left
-- NULL rather than set to '[]' — see the header on what NULL means.
UPDATE pay_ledger pl
   SET pay_items = legs.pay_items
  FROM (
        SELECT DISTINCT ON (ledger_id) ledger_id, pay_items
          FROM llm493_settlement_legs
         ORDER BY ledger_id, audit_id DESC
       ) legs
 WHERE legs.ledger_id = pl.id
   AND pl.pay_items IS NULL;

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
