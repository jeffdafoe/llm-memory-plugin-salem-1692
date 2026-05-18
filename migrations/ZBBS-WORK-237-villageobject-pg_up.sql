-- ZBBS-WORK-237: VillageObjects pg-impl + generation-marker snapshot pattern.
--
-- Slice 9 of the engine rewrite. Two things at once:
--
--   1. Establishes the generation-marker snapshot pattern for v2 state
--      aggregates. Per-row UPSERT scales linearly without array-binding
--      planner issues; per-table sequence stamps a unique gen per
--      checkpoint; trailing DELETE prunes anything absent from the snapshot.
--      Future state-aggregate slices (Structures, Huddles, Scenes,
--      Environment, Actors) reuse the same pattern.
--
--   2. Applies the pattern to VillageObjects + ports the missing v2
--      fields and value conversions.
--
-- Companion design note:
--   shared/tasks/engine-in-memory-rewrite/slice-9-village-objects-pg-design
--
-- Companion settled-design references:
--   shared/tasks/engine-in-memory-rewrite/ownership-and-entry-access
--     (entry_policy mapping + owner column handling, both per PR 4)
--
-- Changes:
--
--   1. snapshot_gen column + sequence + index (gen-marker pattern).
--
--   2. v2-only fields:
--        - available_quantity INTEGER NOT NULL DEFAULT 0 (runtime stock
--          counter for objects with produce/refresh policies — gatherables,
--          vendor inventory)
--        - tags TEXT[] NOT NULL DEFAULT '{}' (per-instance role tags;
--          collapsed from the separate village_object_tag table)
--        - owner_actor_id TEXT NULL (v2 typed owner reference; backfilled
--          from v1 owner string via actor table JOIN)
--
--   3. Tag collapse: village_object_tag rows fold into the new tags[]
--      array column on village_object, then the table is dropped.
--      v2 cascades iterate world.VillageObjects in-memory and filter
--      in Go, so the v1 per-tag SQL index (used by the social
--      scheduler's nearest-match query) doesn't have a v2 consumer.
--      Postgres GIN-indexes text[] natively if a tag query ever
--      materializes.
--
--   4. Owner reference: NEW typed column owner_actor_id, backfilled from
--      the v1 owner string via a JOIN to the actor table. v1 owner is
--      VARCHAR(100) holding login_username per the structure-lookups
--      note, but the v2 type doc-comment drifted to "agent slug" — data
--      could be either login_username (PCs) or llm_memory_agent (NPCs).
--      Backfill tries both columns. v1 owner column LEFT IN PLACE for
--      v1 reader compat during the cutover transition; drop in a later
--      cleanup slice.
--
--   5. entry_policy value migration + CHECK replacement. v1 CHECK is
--      ('none', 'owner', 'anyone'); v2 enum is ('', 'open', 'owner-only',
--      'closed'). Mapping per PR 4's settled design (ownership-and-
--      entry-access codebase note):
--        v1 'none'   → v2 'closed'     (load-bearing "no interior" —
--                                       wells, fountains, decoratives;
--                                       StructureEnter hard-rejects;
--                                       StructureVisit still allowed)
--        v1 'owner'  → v2 'owner-only' (membership check — resident OR
--                                       staff OR owner OR lodger via
--                                       structureMembershipAllows)
--        v1 'anyone' → v2 'open'       ('' is equivalent at runtime;
--                                       entry allowed)
--      New DEFAULT is 'closed' (preserves v1's conservative "no entry
--      concept" default behavior).
--
--   6. Noticeboard content columns DROPPED: content_text and
--      content_posted_at (ZBBS-112). v2 NoticeboardContent stays
--      in-memory only with restart-loss accepted (Jeff EOS-42). Revisit
--      when more per-instance state types proliferate.
--
-- Restart-loss of object_refresh runtime state (AvailableQuantity,
-- LastRefreshAt mutations) is accepted in this slice. v1 object_refresh
-- has 3 columns; v2 ObjectRefresh has 6+ runtime fields — porting needs
-- schema redesign, not just a column port. Separate follow-up slice.
--
-- Rollback caveat: v2 entry_policy '' and 'closed' both reverse to v1
-- 'none' (lossy); operator must drain or map any 'closed' rows before
-- rolling back if v1's CHECK is to be re-imposed. owner_actor_id is
-- simply dropped; v1 owner stays in place so no rename to reverse.

BEGIN;

-- Generation marker for snapshot semantics. Per-checkpoint sequence
-- bump stamps each UPSERT'd row with a unique gen; trailing DELETE
-- prunes rows with stale gen (absent from snapshot). One sequence per
-- aggregate table — no cross-aggregate coordination needed.
CREATE SEQUENCE village_object_snapshot_gen_seq START 1;
ALTER TABLE village_object
    ADD COLUMN snapshot_gen BIGINT NOT NULL DEFAULT 0;
CREATE INDEX idx_village_object_snapshot_gen ON village_object(snapshot_gen);

-- v2-only fields.
ALTER TABLE village_object
    ADD COLUMN available_quantity INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN tags TEXT[] NOT NULL DEFAULT '{}',
    ADD COLUMN owner_actor_id TEXT NULL;

-- Collapse per-tag rows into the array column. COALESCE handles
-- objects with no tags (array_agg returns NULL on empty input).
UPDATE village_object vo SET tags = COALESCE(
    (SELECT array_agg(t.tag ORDER BY t.tag)
       FROM village_object_tag t
      WHERE t.object_id = vo.id),
    '{}'
);
DROP TABLE village_object_tag;

-- Backfill owner_actor_id from v1 owner string. login_username (PCs)
-- is the documented value per structure-lookups; llm_memory_agent
-- (NPCs) is the drifted alternative. LIMIT 1 because either match
-- alone identifies the actor uniquely (both columns are UNIQUE).
-- Rows where v1 owner is NULL or '' (the default for "no owner")
-- skip the lookup and stay NULL.
UPDATE village_object vo
   SET owner_actor_id = (
       SELECT a.id::text
         FROM actor a
        WHERE a.login_username = vo.owner OR a.llm_memory_agent = vo.owner
        LIMIT 1
   )
 WHERE vo.owner IS NOT NULL AND vo.owner <> '';

-- entry_policy value migration + CHECK replacement. v1 enum doesn't
-- include v2's '' (type-driven default — runtime-equivalent to 'open');
-- mapping enumerates v1's three values explicitly and falls through
-- for any unexpected (no-op).
ALTER TABLE village_object DROP CONSTRAINT IF EXISTS village_object_entry_policy_check;
UPDATE village_object SET entry_policy = CASE entry_policy
    WHEN 'none'   THEN 'closed'
    WHEN 'owner'  THEN 'owner-only'
    WHEN 'anyone' THEN 'open'
    ELSE entry_policy
END;
ALTER TABLE village_object ALTER COLUMN entry_policy SET DEFAULT 'closed';
ALTER TABLE village_object ADD CONSTRAINT village_object_entry_policy_check
    CHECK (entry_policy IN ('', 'open', 'owner-only', 'closed'));

-- Drop dormant noticeboard content columns. v2's NoticeboardContent
-- lives in-memory only (World.NoticeboardContent map) and accepts
-- restart-loss per Slice 3 design + Jeff EOS-42 confirmation.
ALTER TABLE village_object DROP COLUMN content_text;
ALTER TABLE village_object DROP COLUMN content_posted_at;

COMMIT;
