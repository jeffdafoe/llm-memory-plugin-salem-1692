-- LLM-128 down: drop the persisted ProductionFocus column.
--
-- Additive-column revert. Same engine-owned caveat as the up: the column is only
-- referenced by the LLM-128 binary's checkpoint UPSERT, so revert the binary
-- first, then drop the column. Rerun-safe via IF EXISTS.

BEGIN;

ALTER TABLE actor DROP COLUMN IF EXISTS production_focus;

COMMIT;
