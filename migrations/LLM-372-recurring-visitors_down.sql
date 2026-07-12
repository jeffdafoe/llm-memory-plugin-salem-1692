-- Revert LLM-372: drop the returning-traveler tier.
-- Dropping recurring_visitor cascades to recurring_visitor_acquaintance via its
-- FK, but drop the child first explicitly for clarity. The visitor tier's soft-ref
-- column + its format CHECK are dropped last. Manual-rollback only (the runner
-- never applies _down.sql). Losing these reverts to the pre-LLM-372 behavior
-- (every traveler is a one-shot stranger; no one returns) — data-clean.
BEGIN;

ALTER TABLE public.visitor
    DROP CONSTRAINT IF EXISTS visitor_recurring_visitor_id_format;
ALTER TABLE public.visitor
    DROP COLUMN IF EXISTS recurring_visitor_id;

DROP TABLE IF EXISTS public.recurring_visitor_acquaintance;
DROP TABLE IF EXISTS public.recurring_visitor;

COMMIT;
