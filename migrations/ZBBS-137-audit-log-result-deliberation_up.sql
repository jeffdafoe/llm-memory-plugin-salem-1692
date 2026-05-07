-- ZBBS-137: widen agent_action_log_result_check to include
-- 'declined' and 'countered'.
--
-- ZBBS-128 step 3 added pay deliberation, which can return
-- payResult.Result = 'declined' or 'countered' (engine/pay.go:481,
-- 512, 541). agent_tick.go:2552-2557 inserts that result into the
-- agent_action_log audit row. The original ZBBS-128 migration
-- shipped a CHECK constraint allowing only ('ok','rejected','failed') —
-- so every declined / countered pay deliberation hits a constraint
-- violation, the audit row is silently dropped (best-effort insert),
-- and only the pay_ledger row preserves the outcome.
--
-- Observed 2026-05-07: ~7 declined/countered events between
-- 2026-05-06 23:02 and 2026-05-07 11:02 with no corresponding audit
-- rows. Engine log: `agent-tick: audit insert <name>/pay: ERROR: new
-- row for relation "agent_action_log" violates check constraint
-- "agent_action_log_result_check"`.
--
-- Fix: widen the constraint to include the two deliberation
-- outcomes. They have distinct semantics from 'rejected' (declined =
-- "no thanks at this price"; countered = "I'd take it for X"); the
-- audit log enum should match the pay_ledger state vocabulary so the
-- two histories align. Per Jeff (via work mail `acd11dc5`), we add
-- only 'declined' and 'countered' — `pending` and `withdrawn` don't
-- have an associated audit insert; `accepted` already maps to today's
-- `'ok'`.

BEGIN;

ALTER TABLE agent_action_log DROP CONSTRAINT agent_action_log_result_check;

ALTER TABLE agent_action_log
    ADD CONSTRAINT agent_action_log_result_check
    CHECK (result = ANY (ARRAY[
        'ok'::text,
        'rejected'::text,
        'failed'::text,
        'declined'::text,
        'countered'::text
    ]));

COMMIT;
