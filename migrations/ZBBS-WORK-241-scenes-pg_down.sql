-- Rollback ZBBS-WORK-241. Pure DROP — Slice 13 was purely additive.

BEGIN;

-- Drop tables before sequences. The current snapshot_gen DEFAULT 0
-- doesn't depend on the sequences, so this migration's reverse is safe
-- either order — but tables-before-sequences is the dependency-safe
-- pattern that survives future edits that might tie a column default
-- to nextval(...) or attach sequence ownership. (code_review R1.)
DROP TABLE IF EXISTS scene_huddle_ref;
DROP TABLE IF EXISTS scene;

DROP SEQUENCE IF EXISTS scene_huddle_ref_snapshot_gen_seq;
DROP SEQUENCE IF EXISTS scene_snapshot_gen_seq;

COMMIT;
