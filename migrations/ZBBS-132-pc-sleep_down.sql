-- ZBBS-132 down: revert PC sleep mechanic.

BEGIN;

DELETE FROM setting WHERE key = 'pc_idle_sleep_minutes';

ALTER TABLE actor
    DROP COLUMN last_pc_input_at,
    DROP COLUMN sleeping_until;

COMMIT;
