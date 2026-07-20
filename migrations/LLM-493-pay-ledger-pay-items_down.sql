-- LLM-493 rollback: drop the settlement goods-legs column.
--
-- Reverting this reverts the price book to recording a mixed coin+goods
-- settlement at its coin leg against the full quantity — the defect the up
-- migration exists to fix. The engine code must be rolled back with it: the
-- seed query's pay_items predicate and the subscriber's goods guard both assume
-- the column, and the seed query will error outright without it.
--
-- No data is recoverable from pay_ledger after this, but nothing is truly lost:
-- every backfilled value was copied from agent_action_log's payload.pay_items,
-- which is untouched and remains the durable audit of what was actually paid.
-- Re-running the up migration reconstructs the column from that source.

BEGIN;

ALTER TABLE pay_ledger
    DROP COLUMN IF EXISTS pay_items;

COMMIT;
