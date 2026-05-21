-- ZBBS-WORK-271 rollback: drop the actor.admin authorization flag.

BEGIN;

ALTER TABLE actor DROP COLUMN IF EXISTS admin;

COMMIT;
