-- Revert LLM-371: drop the traveler rumor payload column.
-- Manual-rollback only (the runner never applies _down.sql). Losing the column
-- reverts to LLM-369 behavior: travelers persist but carry no rumor. Data-clean —
-- the payload is transient traveler state, meaningful only for a visit in flight.
BEGIN;

ALTER TABLE public.visitor
    DROP COLUMN IF EXISTS payload;

COMMIT;
