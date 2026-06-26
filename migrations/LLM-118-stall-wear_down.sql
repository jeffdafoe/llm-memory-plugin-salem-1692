-- LLM-118 down: drop the market-stall wear counter.
--
-- Additive-column revert. Same engine-owned caveat as the up: the column is
-- only referenced by the LLM-118 binary's checkpoint UPSERT, so revert the
-- binary first, then drop the column. Rerun-safe via IF EXISTS.

BEGIN;

ALTER TABLE village_object DROP COLUMN IF EXISTS wear;

COMMIT;
