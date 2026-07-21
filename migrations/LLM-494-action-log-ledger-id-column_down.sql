-- LLM-494 rollback (manual-only; the deploy runner never applies _down.sql).
--
-- Drop the partial index and the ledger_id column. The payload keeps its copy of
-- ledger_id (the _up migration never removed it), so no data is lost — a reverted
-- engine falls back to the jsonb extraction the _up migration converted away from.
-- Pair this with reverting the Go readers, which reference the column.

BEGIN;

DROP INDEX IF EXISTS ix_agent_action_log_paid_ledger_id;

ALTER TABLE agent_action_log
    DROP COLUMN IF EXISTS ledger_id;

COMMIT;
