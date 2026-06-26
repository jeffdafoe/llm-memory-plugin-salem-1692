-- LLM-128: persist a crafter's ProductionFocus across engine restarts.
--
-- A multi-output crafter (the smith: skillet + nail) chooses ONE good to forge
-- via the craft tool; the choice lives on Actor.ProductionFocus and gates which
-- good produce_tick fills. Pre-fix it was in-memory ONLY — no column here, and
-- absent from the checkpoint UPSERT — so every engine restart wiped it: the
-- crafter came back unfocused, the level-triggered production-choice warrant
-- re-fired, and the weak model burned its whole tick iteration budget
-- re-choosing (live: Ezekiel craft x6 after every deploy, LLM-128). Focus is
-- genuine persistent world-state (must survive restart), so it lives durably on
-- the actor row alongside inventory and coins.
--
-- ENGINE-OWNED TABLE — actor is checkpoint-written by the running engine. Purely
-- additive column with a DEFAULT, so no engine stop is required: existing rows
-- backfill to '' (unfocused), and the currently-running binary's checkpoint
-- UPSERT lists explicit columns that do NOT include production_focus, so it
-- leaves the new column at its default until the companion LLM-128 binary
-- deploys. Apply this migration BEFORE deploying that binary (the new UPSERT
-- references production_focus); the standard deploy order (migrate -> restart)
-- does this.
--
-- Rerun-safe via IF NOT EXISTS.

BEGIN;

ALTER TABLE actor ADD COLUMN IF NOT EXISTS production_focus text NOT NULL DEFAULT '';

COMMIT;
