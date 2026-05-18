-- Rollback ZBBS-WORK-239. Reverts the schema to its pre-migration shape
-- but cannot recover discarded v1 huddle data.
--
-- Caveats:
--   - v1 UUID PKs cannot be reconstructed (up migration's DELETE was
--     destructive).
--   - actor.current_huddle_id values stay NULL on the way down.
--   - Outdoor huddles persisted by v2 (structure_id NULL) cannot revert
--     under v1's NOT NULL constraint; operator must DELETE them first.

BEGIN;

-- huddle_member is purely additive in up; drop it.
DROP SEQUENCE IF EXISTS huddle_member_snapshot_gen_seq;
DROP TABLE IF EXISTS huddle_member;

-- Reverse gen-marker bits on scene_huddle.
DROP INDEX IF EXISTS idx_scene_huddle_snapshot_gen;
ALTER TABLE scene_huddle DROP COLUMN IF EXISTS snapshot_gen;
DROP SEQUENCE IF EXISTS huddle_snapshot_gen_seq;

-- Reverse CHECK constraints.
ALTER TABLE scene_huddle DROP CONSTRAINT IF EXISTS scene_huddle_structure_id_nonempty;
ALTER TABLE scene_huddle DROP CONSTRAINT IF EXISTS scene_huddle_id_format;
ALTER TABLE scene_huddle DROP CONSTRAINT IF EXISTS scene_huddle_id_nonempty;

-- Reverse column rename + retypes. Outdoor-huddle rows (structure_id
-- IS NULL) must be drained before this point or the NOT NULL ADD will
-- fail.
ALTER TABLE scene_huddle RENAME COLUMN started_at TO created_at;
ALTER TABLE scene_huddle ALTER COLUMN id TYPE UUID USING id::uuid;
ALTER TABLE scene_huddle ALTER COLUMN id SET DEFAULT gen_random_uuid();
ALTER TABLE scene_huddle ALTER COLUMN structure_id SET NOT NULL;
ALTER TABLE scene_huddle ALTER COLUMN structure_id TYPE UUID USING structure_id::uuid;
ALTER TABLE scene_huddle ADD CONSTRAINT scene_huddle_structure_id_fkey
    FOREIGN KEY (structure_id) REFERENCES village_object(id) ON DELETE CASCADE;

-- Reverse actor.current_huddle_id type + FK. Values are NULL post-up
-- so the cast is trivial.
ALTER TABLE actor ALTER COLUMN current_huddle_id TYPE UUID USING NULL;
ALTER TABLE actor ADD CONSTRAINT actor_current_huddle_id_fkey
    FOREIGN KEY (current_huddle_id) REFERENCES scene_huddle(id) ON DELETE SET NULL;

COMMIT;
