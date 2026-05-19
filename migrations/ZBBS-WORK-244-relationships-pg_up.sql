-- ZBBS-WORK-244: Slice 2 — Actors pg-impl continuity layer
-- (actor_relationship + actor_narrative_state + npc_acquaintance).
--
-- Layers on top of the production schema baseline (migrations/schema.sql)
-- and Slice 1 (ZBBS-WORK-243). Extends the actor aggregate from 3
-- snapshot tiers to 6. The SaveSnapshot side runs all six tiers under the
-- one shared `actor_snapshot` advisory lock; each tier owns its gen-marker.
--
-- Two groups of changes:
--
-- A. dropped_fact_count on actor_relationship. v2-only FIFO-eviction
--    telemetry (Relationship.DroppedFactCount; never decremented).
--    Persisted so the "this pair churns facts faster than consolidation
--    prunes" signal survives restart.
--
--    NOTE: last_consolidated_at is intentionally NOT added here — it
--    already exists in the baseline on both actor_relationship and
--    actor_narrative_state (the per-relationship / per-actor
--    consolidation feature shipped to prod in v1; the v2 cascades
--    re-implement the logic against the same columns).
--
-- B. Snapshot-gen substrate (per Slice 9/10/11/12/13/243 pattern). One
--    column + sequence + index per new tier: actor_relationship,
--    actor_narrative_state, npc_acquaintance. Absent from the baseline
--    (pure v2-rewrite sync bookkeeping).
--
-- npc_acquaintance is NOT renamed to actor_acquaintance. ZBBS-084
-- repointed its FK from npc(id) to actor(id) but left the table name;
-- renaming now would be churn against an unrelated concern and v2 code
-- references the table by name.

BEGIN;

-- A. v2 telemetry column.

ALTER TABLE actor_relationship ADD COLUMN IF NOT EXISTS dropped_fact_count INT NOT NULL DEFAULT 0;

-- B. Snapshot-gen substrate for the three new tiers.

ALTER TABLE actor_relationship    ADD COLUMN IF NOT EXISTS snapshot_gen BIGINT NOT NULL DEFAULT 0;
ALTER TABLE actor_narrative_state ADD COLUMN IF NOT EXISTS snapshot_gen BIGINT NOT NULL DEFAULT 0;
ALTER TABLE npc_acquaintance      ADD COLUMN IF NOT EXISTS snapshot_gen BIGINT NOT NULL DEFAULT 0;

CREATE INDEX IF NOT EXISTS idx_actor_relationship_snapshot_gen    ON actor_relationship(snapshot_gen);
CREATE INDEX IF NOT EXISTS idx_actor_narrative_state_snapshot_gen ON actor_narrative_state(snapshot_gen);
CREATE INDEX IF NOT EXISTS idx_npc_acquaintance_snapshot_gen      ON npc_acquaintance(snapshot_gen);

CREATE SEQUENCE IF NOT EXISTS actor_relationship_snapshot_gen_seq    START 1;
CREATE SEQUENCE IF NOT EXISTS actor_narrative_state_snapshot_gen_seq START 1;
CREATE SEQUENCE IF NOT EXISTS npc_acquaintance_snapshot_gen_seq      START 1;

COMMIT;
