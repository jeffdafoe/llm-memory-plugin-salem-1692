-- ZBBS-WORK-245 down: reverse all changes from _up in inverse order.
-- Drops the snapshot-gen substrate (indexes + columns first, sequences
-- last — dependency-safe teardown). The baseline tables and their
-- pre-existing columns/constraints are left intact; this slice only ever
-- added snapshot_gen bookkeeping.

BEGIN;

DROP INDEX IF EXISTS idx_actor_attribute_snapshot_gen;
DROP INDEX IF EXISTS idx_room_access_snapshot_gen;
DROP INDEX IF EXISTS idx_actor_produce_state_snapshot_gen;
DROP INDEX IF EXISTS idx_actor_dwell_credit_snapshot_gen;

ALTER TABLE actor_attribute     DROP COLUMN IF EXISTS snapshot_gen;
ALTER TABLE room_access         DROP COLUMN IF EXISTS snapshot_gen;
ALTER TABLE actor_produce_state DROP COLUMN IF EXISTS snapshot_gen;
ALTER TABLE actor_dwell_credit  DROP COLUMN IF EXISTS snapshot_gen;

DROP SEQUENCE IF EXISTS actor_attribute_snapshot_gen_seq;
DROP SEQUENCE IF EXISTS room_access_snapshot_gen_seq;
DROP SEQUENCE IF EXISTS actor_produce_state_snapshot_gen_seq;
DROP SEQUENCE IF EXISTS actor_dwell_credit_snapshot_gen_seq;

COMMIT;
