-- Restore the LLM-580 whole-table deferrable UNIQUE on actor.display_name.
--
-- NOT UNCONDITIONALLY REVERSIBLE, by nature rather than by oversight: the up
-- migration permits states this constraint forbids. If any decorative actors
-- have been given the same name while it was in force — which is the entire
-- point of LLM-586 — ADD CONSTRAINT fails here with a duplicate key error and
-- the transaction rolls back, leaving the exclusion constraint in place.
--
-- That is deliberate: silently renaming the operator's ducks to force the
-- rollback through would destroy authored state to satisfy a schema shape.
-- Rename the duplicates first (or drop the surplus decoratives), then re-run.
-- The failure message names the offending value.
--
-- Deferrability is preserved: an immediate constraint wedges the checkpoint's
-- upsert-before-delete ordering. See the up migration for the full reasoning.

BEGIN;

ALTER TABLE public.actor
    DROP CONSTRAINT IF EXISTS actor_display_name_excl;

ALTER TABLE public.actor
    ADD CONSTRAINT actor_display_name_key UNIQUE (display_name)
    DEFERRABLE INITIALLY DEFERRED;

COMMIT;
