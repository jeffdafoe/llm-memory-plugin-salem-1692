-- LLM-686 down: drop the one-row-per-name index.
--
-- The merge is NOT undone: the dropped duplicate rows are gone, and splitting a
-- merged row back into its old visits has no source. The rolled-back engine runs
-- fine on one row per name — it only stops enforcing it.

BEGIN;

DROP INDEX IF EXISTS public.recurring_visitor_name_key;

COMMIT;
