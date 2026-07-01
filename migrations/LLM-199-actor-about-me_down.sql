-- LLM-199 down: drop the per-actor about_me soul column.
--
-- Additive-column revert. Same engine-owned caveat as the up: the column is only
-- referenced by the LLM-199 binary's checkpoint UPSERT, so revert the binary
-- first, then drop the column. Rerun-safe via IF EXISTS.

BEGIN;

ALTER TABLE actor_narrative_state DROP COLUMN IF EXISTS about_me;

COMMIT;
