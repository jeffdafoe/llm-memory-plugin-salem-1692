-- LLM-599 down: drop the per-asset render scale column.
--
-- Any non-default per-asset tuning is lost with the column; the client
-- falls back to its hardcoded 2x for every object sprite, which is the
-- exact pre-LLM-599 rendering.

BEGIN;

ALTER TABLE asset DROP COLUMN render_scale;

COMMIT;
