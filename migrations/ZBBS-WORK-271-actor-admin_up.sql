-- ZBBS-WORK-271: admin flag on actor.
--
-- Adds a boolean `admin` column to the actor table. This is the
-- authorization gate for the upcoming admin/editor write routes
-- (force-phase, object reposition/delete) on the engine's HTTP surface:
-- those routes resolve the caller's actor by login_username (the same
-- path pc/* uses) and require admin = true.
--
-- Externally managed, NOT sim state. The engine never mutates this — it
-- is set directly in the DB for the human operators who administer the
-- village. Accordingly the checkpoint UPSERT in pg.SaveWorld deliberately
-- does NOT write this column (only LoadWorld reads it), so a checkpoint
-- save can never clobber an operator-set value. DEFAULT false means every
-- existing and future actor is a non-admin until explicitly promoted.

BEGIN;

ALTER TABLE actor ADD COLUMN IF NOT EXISTS admin BOOLEAN NOT NULL DEFAULT false;

COMMIT;
