-- LLM-371: give in-flight travelers a grounded rumor to carry.
--
-- The transient-visitor framework (engine/sim/visitor.go) now selects one
-- grounded rumor at spawn from the in-memory action log — a diegetic, past-tense
-- clause about a real recent village beat ("Ezekiel Crane turned out a plow for
-- the Hale farm") — and voices it through the salem-visitor identity preface
-- (engine/sim/perception/render.go renderTravelerPreface). The clause rides on
-- VisitorState.Payload.
--
-- This column is the durable home for that clause, added to the existing visitor
-- checkpoint tier (LLM-369) rather than a new table: the payload is a field of
-- already-persisted VisitorState, not a subsystem of its own. It must persist
-- because the action log it was drawn from is in-memory and restart-wiped, so
-- re-selecting on rehydrate would draw from an empty pool — and Salem restarts on
-- every deploy, so an unpersisted rumor would blank out on each one. The
-- generation-marker UPSERT / DELETE in engine/sim/repo/pg/visitors.go carries it
-- alongside the other typed columns.
--
-- NOT NULL DEFAULT '' — an empty string is the well-defined "no rumor-worthy beat
-- was on hand at spawn" value; the preface drops the clause on empty. The default
-- also backfills any visitor row already checkpointed by an LLM-369 engine that
-- predates this column, so the ADD is a clean no-op-safe rewrite.
--
-- Engine-checkpointed standalone aggregate → deploy stop -> migrate -> start.
-- IF NOT EXISTS so a re-run (or a future re-baseline that folds this into
-- schema.sql, then replays) is a clean no-op under ON_ERROR_STOP=1.
BEGIN;

ALTER TABLE public.visitor
    ADD COLUMN IF NOT EXISTS payload text NOT NULL DEFAULT '';

COMMIT;
