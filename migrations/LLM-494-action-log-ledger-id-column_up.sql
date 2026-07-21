-- LLM-494: promote agent_action_log.ledger_id out of the jsonb payload into a
-- real typed bigint column + a partial index.
--
-- WHY. agent_action_log records a ledger_id for settlement-adjacent rows (paid,
-- and the offered / declined / countered haggle beats), but only INSIDE the
-- payload jsonb. Every reader re-extracts it as payload->>'ledger_id' with its
-- own guard, and two of those readers are live:
--
--   1. MaxPaidActionLogLedgerID (orders.go) runs at EVERY engine boot. It floors
--      the pay-ledger id allocator from GREATEST(MaxLedgerID, this) so a restart
--      cannot re-mint a consume_now id whose only durable trace is this log
--      (LLM-245). In the payload shape it is an unindexable sequential scan with
--      a per-row regex + cast, and it slows as the log grows.
--   2. The /umbilical/settlements filter (LoadSettlements) matched
--      payload->>'ledger_id' = $N as TEXT against a decimal-formatted id — a
--      string stand-in for a numeric compare that silently returns nothing if a
--      value's text form ever drifts (a quoted value, a leading zero, a foreign
--      writer).
--
-- A typed column makes the boot query an index max() and the filter a genuine
-- numeric compare, and collapses the hand-rolled extractions to one column.
--
-- UNIVERSAL MIRROR, NOT PAID-ONLY. The column reflects payload.ledger_id for
-- every row that carries a numeric one, not just paid rows. That gives the
-- simplest invariant — the column equals the payload extraction on every row —
-- which is exactly what the backfill and its test assert, and it lets the whole
-- haggle (offered / countered / declined / paid) be reconstructed by a typed
-- join (LLM-283's durable-mirror goal). The readers stay action_type='paid'
-- scoped; the broader column costs them nothing.
--
-- THE CAST NEEDS A CASE, NOT A WHERE. Postgres does not guarantee a WHERE
-- predicate is evaluated before a SET-list expression, so a ::bigint cast in the
-- SET can be reached for a row the WHERE would exclude and RAISE on an
-- out-of-range value — aborting the migration (LLM-493, code_review). The cast
-- therefore lives inside a CASE, whose arms evaluate in order with short-circuit:
-- the anchored, length-bounded regex runs first, so ::bigint is reached only for
-- a canonical decimal already known to fit. This is bounded work — the regex
-- rejects an out-of-range or arbitrarily long string outright, with no
-- arbitrary-precision numeric parse (matching the old ::bigint reader, which
-- likewise failed fast). The <= 18-digit arm always fits bigint; the 19-digit arm
-- range-checks lexicographically against bigint's max (equal length, so string
-- order equals numeric order) before casting.
--
-- This preserves the old reader's semantics for every id it could represent. A
-- value <= bigint max backfills to the same number the old (...)::bigint produced
-- — including a valid 19-digit id, which a blanket length cap would have dropped,
-- regressing the floor. A digits-only value ABOVE bigint's range, on which the old
-- unguarded boot query would have RAISED and wedged boot, lands NULL here instead:
-- strictly safer, and it cannot lower the floor for any representable id. ledger_id
-- is engine-written as a canonical JSON number, so a leading-zero or 20+ digit
-- form does not occur; such a value maps to NULL like any malformed one.
--
-- PARTIAL INDEX scoped to the two paid readers. Both the boot max() and the
-- settlements filter are action_type='paid'. A partial index on (ledger_id)
-- WHERE action_type='paid' AND ledger_id IS NOT NULL turns the boot max() into a
-- backward index scan (LIMIT 1) and serves the filter's equality seek, without
-- indexing the non-paid haggle rows no query looks up by id.
--
-- ENGINE-WRITTEN TABLE. agent_action_log is appended by the running engine.
-- Apply with the engine STOPPED (stop -> migrate -> start, the standard deploy
-- order). ADD COLUMN is metadata-only and the backfill writes only the new
-- column, so it is safe regardless, but keep the standard order.
--
-- Rerun-safe: ADD COLUMN IF NOT EXISTS, backfill gated on ledger_id IS NULL,
-- CREATE INDEX IF NOT EXISTS.

BEGIN;

ALTER TABLE agent_action_log
    ADD COLUMN IF NOT EXISTS ledger_id bigint;

-- Backfill the typed column from the payload for every row that carries a
-- ledger_id bigint can hold, using the SAME guarded extraction the write path
-- uses (insertActionLogSQL) so the column matches on every row. The CASE guards
-- the cast (see header); the WHERE regex — a total function, safe in any
-- evaluation order — limits the rewrite to plausibly-in-range rows (<= 19 digits)
-- and makes a re-run a no-op (their ledger_id is already set). A 19-digit row
-- above bigint's max is touched but its CASE yields NULL; for an append-only
-- table it then stays NULL.
UPDATE agent_action_log
   SET ledger_id = CASE
           WHEN payload->>'ledger_id' ~ '^[0-9]{1,18}$'
               THEN (payload->>'ledger_id')::bigint
           WHEN payload->>'ledger_id' ~ '^[0-9]{19}$'
            AND payload->>'ledger_id' <= '9223372036854775807'
               THEN (payload->>'ledger_id')::bigint
       END
 WHERE ledger_id IS NULL
   AND payload->>'ledger_id' ~ '^[0-9]{1,19}$';

-- Partial index tailored to the two paid-scoped readers (boot max + settlements
-- filter). Non-paid haggle rows carry a ledger_id in the column but are not
-- indexed — no query seeks them by id.
CREATE INDEX IF NOT EXISTS ix_agent_action_log_paid_ledger_id
    ON agent_action_log (ledger_id)
    WHERE action_type = 'paid' AND ledger_id IS NOT NULL;

COMMIT;
