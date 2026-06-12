-- ZBBS-HOME-449 down: drop the checkpointed walk destination.

BEGIN;

ALTER TABLE public.actor
    DROP COLUMN move_destination;

COMMIT;
