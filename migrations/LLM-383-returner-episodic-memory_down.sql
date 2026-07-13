-- Revert LLM-383: drop the returner episodic-memory columns from
-- recurring_visitor_acquaintance. Reverts to LLM-372 coarse familiarity only
-- (met-before + recency; no remembered specifics) — data-clean (only the folded
-- episodic memory is lost; the pair, timestamps, and persona are untouched).
-- Manual-rollback only (the runner never applies _down.sql).
BEGIN;

ALTER TABLE public.recurring_visitor_acquaintance
    DROP CONSTRAINT IF EXISTS recurring_visitor_acquaintance_salient_facts_bounded;
ALTER TABLE public.recurring_visitor_acquaintance
    DROP CONSTRAINT IF EXISTS recurring_visitor_acquaintance_summary_sane;

ALTER TABLE public.recurring_visitor_acquaintance
    DROP COLUMN IF EXISTS salient_facts;
ALTER TABLE public.recurring_visitor_acquaintance
    DROP COLUMN IF EXISTS summary_text;
ALTER TABLE public.recurring_visitor_acquaintance
    DROP COLUMN IF EXISTS last_consolidated_at;

COMMIT;
