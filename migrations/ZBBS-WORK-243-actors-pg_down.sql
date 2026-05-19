-- ZBBS-WORK-243 down: reverse all changes from _up in inverse order.

BEGIN;

-- C. Revert next_self_tick_at to TIMESTAMP without time zone. Convert
-- back via AT TIME ZONE 'UTC' so the wall-clock value matches what
-- the column held pre-migration (the value was stored as UTC by the
-- original USING clause). Guarded for partially-applied recovery.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM information_schema.columns
        WHERE table_name = 'actor'
          AND column_name = 'next_self_tick_at'
          AND data_type = 'timestamp with time zone'
    ) THEN
        ALTER TABLE actor
            ALTER COLUMN next_self_tick_at TYPE TIMESTAMP
            USING next_self_tick_at AT TIME ZONE 'UTC';
    END IF;
END $$;

-- B. Drop v2-state columns.
ALTER TABLE actor DROP COLUMN IF EXISTS move_attempt_counter;
ALTER TABLE actor DROP COLUMN IF EXISTS sim_state;
ALTER TABLE actor DROP COLUMN IF EXISTS sim_state_entered_at;

-- A. Drop snapshot-gen substrate — indexes + columns first, sequences
-- last. The sequences don't currently own / default the columns, so
-- the original order worked, but dependency-safe teardown survives any
-- future edit that wires DEFAULT nextval(...) or ownership.
DROP INDEX IF EXISTS idx_actor_inventory_snapshot_gen;
DROP INDEX IF EXISTS idx_actor_need_snapshot_gen;
DROP INDEX IF EXISTS idx_actor_snapshot_gen;

ALTER TABLE actor_inventory DROP COLUMN IF EXISTS snapshot_gen;
ALTER TABLE actor_need      DROP COLUMN IF EXISTS snapshot_gen;
ALTER TABLE actor           DROP COLUMN IF EXISTS snapshot_gen;

DROP SEQUENCE IF EXISTS actor_inventory_snapshot_gen_seq;
DROP SEQUENCE IF EXISTS actor_need_snapshot_gen_seq;
DROP SEQUENCE IF EXISTS actor_snapshot_gen_seq;

COMMIT;
