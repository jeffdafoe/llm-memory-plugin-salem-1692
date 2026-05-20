-- Rollback ZBBS-WORK-238. Drops only the gen-marker substrate this
-- migration adds (snapshot_gen index + column + sequence). The
-- supply/regen/dwell columns and CHECKs are NOT touched here — they
-- belong to the prod baseline (ZBBS-090 / ZBBS-172), not this migration.

BEGIN;

DROP INDEX IF EXISTS idx_object_refresh_snapshot_gen;
ALTER TABLE object_refresh DROP COLUMN IF EXISTS snapshot_gen;
DROP SEQUENCE IF EXISTS object_refresh_snapshot_gen_seq;

COMMIT;
