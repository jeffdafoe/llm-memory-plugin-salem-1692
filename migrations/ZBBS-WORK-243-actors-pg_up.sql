-- ZBBS-WORK-243: Slice 1 — Actors pg-impl (parent + needs + inventory).
--
-- Three groups of changes:
--
-- A. Snapshot-gen substrate (per Slice 9/10/11/12/13 pattern). One
--    column + sequence + index per table — three tiers in this slice:
--    parent `actor`, `actor_need`, `actor_inventory`. Each tier owns
--    its own gen-marker; the SaveSnapshot side runs one shared advisory
--    lock per actor aggregate.
--
-- B. v2-state columns on `actor` for restart-resume:
--      - move_attempt_counter — MoveIntent itself is deliberately NOT
--        persisted (visitor precedent: restart-loss of in-flight
--        movement acceptable, next reactor tick re-dispatches), but the
--        per-actor monotonic counter MUST persist so post-restart
--        MovementAttemptIDs can't collide with pre-restart attempt IDs
--        still referenced by event logs.
--      - sim_state / sim_state_entered_at — sim.Actor declares "State
--        itself is checkpointed so restart resumes in the same state."
--        Soft FSM — Go type is the authoritative allowlist, no DB-side
--        CHECK constraint (a CHECK that refused Go-side bugs would
--        create a "perpetual no-persist loop" failure mode). `_entered_at`
--        anchors the state-stuck-for-how-long telemetry.
--
--    Name prefix `sim_` marks these as v2-rewrite-owned and
--    disambiguates them from v1's many `*_at`/`*_until` timestamps. If
--    State turns out to be derivable post-load, the prefix flags them
--    as v2-private and safe to drop without breaking v1 consumers.
--
-- C. Housekeeping: next_self_tick_at TIMESTAMP → TIMESTAMPTZ. It's the
--    only timestamp column on `actor` that wasn't already TZ-aware. v1
--    readers all do `WHERE next_self_tick_at <= now()`; pre/post
--    behavior is byte-identical when the PG session TZ is UTC (standard
--    salem deploy posture). Explicit USING anchors the conversion to
--    UTC rather than relying on session-TZ defaults during the ALTER.

BEGIN;

-- A. Snapshot-gen substrate.

ALTER TABLE actor             ADD COLUMN IF NOT EXISTS snapshot_gen BIGINT NOT NULL DEFAULT 0;
ALTER TABLE actor_need        ADD COLUMN IF NOT EXISTS snapshot_gen BIGINT NOT NULL DEFAULT 0;
ALTER TABLE actor_inventory   ADD COLUMN IF NOT EXISTS snapshot_gen BIGINT NOT NULL DEFAULT 0;

CREATE INDEX IF NOT EXISTS idx_actor_snapshot_gen           ON actor(snapshot_gen);
CREATE INDEX IF NOT EXISTS idx_actor_need_snapshot_gen      ON actor_need(snapshot_gen);
CREATE INDEX IF NOT EXISTS idx_actor_inventory_snapshot_gen ON actor_inventory(snapshot_gen);

CREATE SEQUENCE IF NOT EXISTS actor_snapshot_gen_seq           START 1;
CREATE SEQUENCE IF NOT EXISTS actor_need_snapshot_gen_seq      START 1;
CREATE SEQUENCE IF NOT EXISTS actor_inventory_snapshot_gen_seq START 1;

-- B. v2-state columns.

ALTER TABLE actor ADD COLUMN IF NOT EXISTS move_attempt_counter BIGINT NOT NULL DEFAULT 0;
ALTER TABLE actor ADD COLUMN IF NOT EXISTS sim_state            VARCHAR(32) NOT NULL DEFAULT 'idle';
ALTER TABLE actor ADD COLUMN IF NOT EXISTS sim_state_entered_at TIMESTAMPTZ NOT NULL DEFAULT now();

-- C. TIMESTAMP → TIMESTAMPTZ housekeeping. Guarded so the migration
-- survives partial-apply recovery (TYPE already TIMESTAMPTZ from a
-- prior run).
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM information_schema.columns
        WHERE table_name = 'actor'
          AND column_name = 'next_self_tick_at'
          AND data_type = 'timestamp without time zone'
    ) THEN
        ALTER TABLE actor
            ALTER COLUMN next_self_tick_at TYPE TIMESTAMPTZ
            USING next_self_tick_at AT TIME ZONE 'UTC';
    END IF;
END $$;

COMMIT;
