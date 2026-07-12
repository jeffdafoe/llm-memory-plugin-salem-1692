-- Revert LLM-373: drop the traveler day-plan column.
--
-- Manual-rollback only (the runner never applies _down.sql). Dropping `plan`
-- reverts to the pre-LLM-373 behavior: a rehydrated traveler loses its in-flight
-- itinerary, its pack/purse, and any booked-room grant (it re-seeds empty, as it
-- did before this ticket). Data-clean — the visitor tier is a transient mirror.
BEGIN;

ALTER TABLE public.visitor
    DROP COLUMN IF EXISTS plan;

COMMIT;
