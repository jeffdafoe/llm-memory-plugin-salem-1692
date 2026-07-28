-- Revert LLM-547: drop the per-pair conversational recency trail.
--
-- Manual-rollback only (the runner never applies _down.sql). Data-clean: losing
-- these rows reverts to the pre-LLM-547 behaviour, where an actor is never told
-- who it has already spoken with. The in-memory ledger is unaffected until the
-- engine restarts, at which point it simply comes back empty and refills from
-- live conversation.
BEGIN;

DROP TABLE IF EXISTS public.actor_contact;

COMMIT;
