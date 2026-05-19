-- ZBBS-WORK-237 down: reverse VillageObjects pg-impl + generation-marker.
--
-- Rollback caveats:
--   - v2 entry_policy '' and 'closed' both reverse to v1 'none'. If v2
--     wrote any 'closed' rows whose intent was distinct from "no entry
--     concept", that distinction is lost on reversal.
--   - owner_actor_id values are dropped. The v1 owner column was left
--     in place by the up-migration, so login_username-shaped data is
--     preserved; non-PC owner_actor_id values that never had a v1 owner
--     equivalent are gone.
--   - content_text / content_posted_at columns are re-added empty. v2
--     accepted restart-loss for noticeboard content; there's nothing to
--     restore.
--   - tags array values are exploded back into per-row village_object_tag
--     entries. Any tags v2 stored that exceed v1's VARCHAR(64) per-tag
--     limit will fail the column type — operator must scan + truncate
--     before rolling back if v2 added long tags.

BEGIN;

-- Restore noticeboard content columns (no data to restore).
ALTER TABLE village_object
    ADD COLUMN content_text TEXT NULL,
    ADD COLUMN content_posted_at TIMESTAMP NULL;

-- Restore entry_policy v1 values + CHECK. The reverse mapping is lossy
-- on v2's '' → v1 'none' fallthrough; same for v2 'closed' → v1 'none'.
ALTER TABLE village_object DROP CONSTRAINT IF EXISTS village_object_entry_policy_check;
UPDATE village_object SET entry_policy = CASE entry_policy
    WHEN ''           THEN 'none'
    WHEN 'closed'     THEN 'none'
    WHEN 'open'       THEN 'anyone'
    WHEN 'owner-only' THEN 'owner'
    ELSE entry_policy
END;
ALTER TABLE village_object ALTER COLUMN entry_policy SET DEFAULT 'none';
ALTER TABLE village_object ADD CONSTRAINT village_object_entry_policy_check
    CHECK (entry_policy IN ('none', 'owner', 'anyone'));

-- Drop owner_actor_id (v1 owner column stayed in place — nothing to rename).
ALTER TABLE village_object DROP COLUMN owner_actor_id;

-- Drop tags NULL-element CHECK (added in the up migration).
ALTER TABLE village_object DROP CONSTRAINT IF EXISTS village_object_tags_no_nulls;

-- Recreate village_object_tag and explode tags array back into rows.
CREATE TABLE village_object_tag (
    object_id UUID NOT NULL REFERENCES village_object(id) ON DELETE CASCADE,
    tag VARCHAR(64) NOT NULL,
    PRIMARY KEY (object_id, tag)
);
CREATE INDEX idx_village_object_tag_tag ON village_object_tag(tag);
-- DISTINCT defends against duplicate tags in the array (no UNIQUE
-- constraint on the v2 column); NOT NULL filter defends against any
-- NULL elements that bypassed the up migration's CHECK constraint
-- (shouldn't happen, but down migration shouldn't compound a prior
-- failure). Long tags (>VARCHAR(64)) still fail — documented in the
-- caveat block above.
INSERT INTO village_object_tag (object_id, tag)
    SELECT DISTINCT vo.id, t
      FROM village_object vo
 CROSS JOIN unnest(vo.tags) AS t
     WHERE t IS NOT NULL;

-- Drop v2-only fields.
ALTER TABLE village_object
    DROP COLUMN tags,
    DROP COLUMN available_quantity;

-- Drop generation marker.
DROP INDEX IF EXISTS idx_village_object_snapshot_gen;
ALTER TABLE village_object DROP COLUMN snapshot_gen;
DROP SEQUENCE village_object_snapshot_gen_seq;

COMMIT;
