-- ZBBS-110 down: drop scheduled self-tick columns + index.

BEGIN;

DROP INDEX IF EXISTS idx_actor_next_self_tick_at;

ALTER TABLE actor
    DROP COLUMN next_self_tick_reason,
    DROP COLUMN next_self_tick_at;

COMMIT;
