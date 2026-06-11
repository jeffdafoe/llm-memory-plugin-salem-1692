-- ZBBS-WORK-399 down: drop the narration_pool_expansion table.
--
-- Destroys all LLM-expanded narration lines — the engine falls back to
-- the compile-time seed pools on next boot (the merge read is non-fatal
-- when the table is missing only in the sense that main.go logs and
-- continues; run this only together with reverting the engine commit).

BEGIN;

DROP TABLE public.narration_pool_expansion;

COMMIT;
