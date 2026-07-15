-- LLM-412 down: drop the hearth fire-state column.
--
-- Manual-rollback-only (the runner never applies _down files). Loses only
-- transient-in-spirit state — which fires are currently lit — so the rollback
-- cost is every fire going out once.

BEGIN;

ALTER TABLE village_object DROP COLUMN IF EXISTS hearth_lit_until;

COMMIT;
