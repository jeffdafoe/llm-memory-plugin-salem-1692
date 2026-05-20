-- ZBBS-WORK-245: Slice 3 — Actors pg-impl final tier
-- (actor_dwell_credit + actor_produce_state + room_access + actor_attribute).
--
-- Layers on top of the production schema baseline (migrations/schema.sql),
-- Slice 1 (ZBBS-WORK-243), and Slice 2 (ZBBS-WORK-244). Extends the actor
-- aggregate from 6 snapshot tiers to 10. SaveSnapshot runs all ten tiers
-- under the one shared `actor_snapshot` advisory lock; each tier owns its
-- own gen-marker.
--
-- Pure snapshot-gen substrate (per Slice 9/10/11/12/13/243/244 pattern).
-- One column + sequence + index per new tier. All four columns are absent
-- from the baseline (pure v2-rewrite sync bookkeeping), so every ADD is
-- IF NOT EXISTS-guarded and applies cleanly on the prod-derived baseline.
--
-- No DB CHECK is added on snapshot_gen (it's bookkeeping, not engine
-- output) and none is added on the engine-output columns — Go owns those
-- invariants (the sim_state "perpetual no-persist loop" rationale; see the
-- actors-pg codebase note). The existing baseline CHECKs on
-- actor_dwell_credit (dwell_delta < 0, source allowlist, remaining↔source
-- pairing) are left untouched; the repo's pre-pass validates the same
-- shape in Go so a violation surfaces as a clean substrate rejection
-- rather than a mid-Tx CHECK failure.
--
-- Tables retain their baseline names. room_access keeps its pre-rename
-- `subspace_access_pkey` PK constraint name (ZBBS-149 renamed the table
-- but not the constraint); v2 references the table by its current name.

BEGIN;

ALTER TABLE actor_dwell_credit  ADD COLUMN IF NOT EXISTS snapshot_gen BIGINT NOT NULL DEFAULT 0;
ALTER TABLE actor_produce_state ADD COLUMN IF NOT EXISTS snapshot_gen BIGINT NOT NULL DEFAULT 0;
ALTER TABLE room_access         ADD COLUMN IF NOT EXISTS snapshot_gen BIGINT NOT NULL DEFAULT 0;
ALTER TABLE actor_attribute     ADD COLUMN IF NOT EXISTS snapshot_gen BIGINT NOT NULL DEFAULT 0;

CREATE INDEX IF NOT EXISTS idx_actor_dwell_credit_snapshot_gen  ON actor_dwell_credit(snapshot_gen);
CREATE INDEX IF NOT EXISTS idx_actor_produce_state_snapshot_gen ON actor_produce_state(snapshot_gen);
CREATE INDEX IF NOT EXISTS idx_room_access_snapshot_gen         ON room_access(snapshot_gen);
CREATE INDEX IF NOT EXISTS idx_actor_attribute_snapshot_gen     ON actor_attribute(snapshot_gen);

CREATE SEQUENCE IF NOT EXISTS actor_dwell_credit_snapshot_gen_seq  START 1;
CREATE SEQUENCE IF NOT EXISTS actor_produce_state_snapshot_gen_seq START 1;
CREATE SEQUENCE IF NOT EXISTS room_access_snapshot_gen_seq         START 1;
CREATE SEQUENCE IF NOT EXISTS actor_attribute_snapshot_gen_seq     START 1;

COMMIT;
