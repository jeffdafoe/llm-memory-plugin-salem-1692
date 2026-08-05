-- LLM-599 down: drop the per-asset render scale column.
--
-- Any non-default per-asset tuning is lost with the column; the client
-- falls back to its hardcoded 2x for every object sprite, which is the
-- exact pre-LLM-599 rendering.

BEGIN;

-- IF EXISTS makes a rerun a no-op. Dropping the column also drops its
-- asset_render_scale_positive_finite CHECK.
ALTER TABLE asset DROP COLUMN IF EXISTS render_scale;

COMMIT;
