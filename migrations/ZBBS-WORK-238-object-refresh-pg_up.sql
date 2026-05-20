-- ZBBS-WORK-238: object_refresh pg-impl — gen-marker substrate only.
--
-- Slice 10 of the engine rewrite. Closes the deferred carry-forward
-- from Slice 9 (ZBBS-WORK-237): object_refresh becomes a checkpointed
-- child of village_object, co-managed by VillageObjectsRepo under the
-- same SaveSnapshot Tx and sharing the parent's advisory lock.
--
-- SCHEMA RECONCILIATION (2026-05-20). This migration originally also
-- added the supply/regen/dwell columns (available_quantity, max_quantity,
-- refresh_mode, refresh_period_hours, last_refresh_at, dwell_*) and their
-- CHECK constraints. That was a fork: HOME had already shipped the same
-- feature to prod (ZBBS-090 supply/regen + ZBBS-172 dwell-recovery), so
-- those columns + CHECKs + the refresh_attribute FK live in the prod
-- baseline (migrations/schema.sql) as the canonical shape. Prod is
-- canonical (confirmed via git provenance + HOME); the branch conforms to
-- it. The forked DDL was removed here — re-adding columns the baseline
-- already has fails the integration harness with "column already exists".
--
-- Net of reconciliation, the ONLY object_refresh change the rewrite
-- branch owns that isn't in the prod baseline is the gen-marker
-- snapshot_gen substrate. That's all this migration does now.
--
-- Note on prod vs branch shape (handled on the Go side, not here):
--   - prod refresh_mode is NOT NULL DEFAULT 'continuous' (the
--     finite/infinite discriminant is available_quantity IS NULL, not
--     mode). VillageObjectsRepo.SaveSnapshot writes 'continuous' for
--     infinite rows so the NOT NULL holds.
--   - prod's config-rate column is dwell_amount (smallint); the per-actor
--     credit snapshot column dwell_delta lives on actor_dwell_credit.
--
-- Companion design / pattern references:
--   shared/notes/codebase/salem-engine-v2/pg-snapshot-pattern
--   shared/notes/codebase/salem-engine-v2/village-objects-pg (Refreshes section)

BEGIN;

-- Gen-marker pattern column + sequence + index. Owns its own sequence
-- (object_refresh_snapshot_gen_seq) — an independent tier counter from
-- the parent's village_object_snapshot_gen_seq. Shares the parent's
-- advisory lock (acquired by VillageObjectsRepo.SaveSnapshot at the start
-- of the same Tx), so no separate lock for this table.
CREATE SEQUENCE object_refresh_snapshot_gen_seq START 1;
ALTER TABLE object_refresh
    ADD COLUMN snapshot_gen BIGINT NOT NULL DEFAULT 0;
CREATE INDEX idx_object_refresh_snapshot_gen ON object_refresh(snapshot_gen);

COMMIT;
