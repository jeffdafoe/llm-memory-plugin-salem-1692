-- Rollback ZBBS-WORK-241. Pure DROP — Slice 13 was purely additive.

BEGIN;

-- Drop child first (parent FK depends on it indirectly via CASCADE).
DROP SEQUENCE IF EXISTS scene_huddle_ref_snapshot_gen_seq;
DROP TABLE IF EXISTS scene_huddle_ref;

DROP SEQUENCE IF EXISTS scene_snapshot_gen_seq;
DROP TABLE IF EXISTS scene;

COMMIT;
