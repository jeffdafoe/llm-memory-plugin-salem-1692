-- LLM-118: durable Wear counter on market-stall village objects.
--
-- A wooden market stall accrues `wear` in proportion to the coin its owner
-- turns over at it (engine: commitPayTransfer). Crossing the repair threshold
-- warrants a repair; crossing the degrade threshold closes the stall for trade
-- until mended; a repair resets wear to 0. Wear is genuine persistent
-- world-state (accumulates over hours, must survive restart, is queryable), so
-- it lives durably on the village_object row, not in memory.
--
-- ENGINE-OWNED TABLE — village_object is checkpoint-written by the running
-- engine. This is a PURELY ADDITIVE column with a DEFAULT, so no engine stop is
-- required: existing rows backfill to 0, and the currently-running binary's
-- checkpoint UPSERT lists explicit columns that do NOT include `wear`, so it
-- leaves the new column at its default until the companion LLM-118 binary
-- deploys. Apply this migration BEFORE deploying that binary (the new UPSERT
-- references `wear`); the standard deploy order (migrate -> restart) does this.
--
-- Rerun-safe via IF NOT EXISTS.

BEGIN;

ALTER TABLE village_object ADD COLUMN IF NOT EXISTS wear integer NOT NULL DEFAULT 0;

COMMIT;
