-- ZBBS-WORK-239: Huddles pg-impl + child-table gen-marker (Slice 11).
--
-- Persists sim.Huddle + its Members set against Postgres. First non-
-- VillageObjects child-table application of the gen-marker snapshot
-- pattern (Slice 10 established the precedent on object_refresh).
--
-- Companion design note:
--   shared/tasks/engine-in-memory-rewrite/slice-11-huddles-pg-design
-- Companion pattern reference:
--   shared/notes/codebase/salem-engine-v2/pg-snapshot-pattern
--
-- Major schema changes:
--
--   1. scene_huddle.id changes from UUID (gen_random_uuid) to TEXT —
--      v2 mints HuddleIDs as 'hud-<32 hex>' strings via
--      engine/sim/huddle_commands.go::newHuddleID. UUID column can't
--      hold the prefixed format. Slice 5 precedent for v2's
--      heterogeneous string IDs.
--
--   2. scene_huddle.structure_id changes from UUID NOT NULL FK
--      village_object(id) to TEXT NULL — v2 outdoor huddles (per
--      pr-4a-scene-bound-design) have empty StructureID, and v2
--      Huddle.StructureID conceptually refs v2 Structure (a separate
--      aggregate from VillageObject; pg-impl not yet shipped). TEXT
--      soft ref per Slice 5 cross-aggregate pattern.
--
--   3. created_at renamed to started_at — matches the v2
--      sim.Huddle.StartedAt field name; clarity at the DB layer.
--
--   4. actor.current_huddle_id FK dropped + column retyped to TEXT
--      NULL — same Slice 5 posture (drop cross-aggregate FKs). Done
--      in this migration because we're already touching the column's
--      referential integrity. Actors-pg-impl handles future writes
--      from the loaded huddle_member set.
--
--   4b. (reconciliation 2026-05-20) agent_action_log.huddle_id,
--      pay_ledger.huddle_id, scene_quote.huddle_id FKs dropped + columns
--      retyped UUID→TEXT — same posture as §4. These prod-baseline FK
--      referrers of scene_huddle.id postdated the original draft; the
--      scene_huddle.id retype (§2) can't proceed while a uuid FK
--      references it. Soft-ref TEXT, no FK re-add.
--
--   5. New child table huddle_member (huddle_id, actor_id) for
--      Huddle.Members. UNIQUE(actor_id) enforces the v2 single-active-
--      huddle-per-actor invariant. ConcludeHuddle wipes Huddle.Members
--      in memory (engine/sim/huddle_commands.go:480), so concluded
--      huddles produce zero member rows — current-membership-only
--      semantic.
--
--   6. snapshot_gen + sequence on both tables (gen-marker pattern).
--      Independent gen tiers; shared advisory lock at parent.
--
-- v1 huddle state is DISCARDED, not migrated:
--   - UUID PKs cannot be reformatted to 'hud-<hex>' without minting
--     fresh IDs, which would invalidate the actor.current_huddle_id
--     back-references anyway.
--   - Village is offline per project_village_stopped — no live
--     huddles to preserve.
--   - Huddles are ephemeral by design; no logical loss.
--   UPDATE actor SET current_huddle_id = NULL + DELETE FROM
--   scene_huddle handle this before the schema changes.
--
-- ID-format CHECK constraint scene_huddle_id_format enforces
-- 'hud-[0-9a-f]{32}' — coupled to engine/sim/huddle_commands.go::
-- newHuddleID. If that function's output format changes, this CHECK
-- must be updated in the same migration. Documented in
-- shared/notes/codebase/salem-engine-v2/huddles-pg.
--
-- Rollback caveats:
--   - v1 UUID PKs can't be reconstructed (discard is destructive)
--   - actor.current_huddle_id reverts to UUID but values stay NULL
--   - Outdoor huddles (structure_id NULL) cannot revert under v1's
--     NOT NULL constraint; operator must drain them before rolling
--     back.

BEGIN;

-- §3 (design): Discard v1 huddle state.
UPDATE actor SET current_huddle_id = NULL;
DELETE FROM scene_huddle;

-- §4 (design): Drop FK + retype actor.current_huddle_id.
ALTER TABLE actor DROP CONSTRAINT IF EXISTS actor_current_huddle_id_fkey;
ALTER TABLE actor ALTER COLUMN current_huddle_id TYPE TEXT USING NULL;

-- §4b (reconciliation 2026-05-20): Drop + retype the remaining prod-baseline
-- FK referrers of scene_huddle.id. The canonical prod baseline carries three
-- uuid FKs to scene_huddle(id) that this migration's original draft predated:
-- agent_action_log.huddle_id and pay_ledger.huddle_id (both ON DELETE SET
-- NULL), and scene_quote.huddle_id (ON DELETE CASCADE; part of
-- scene_quote_pkey). Retyping scene_huddle.id to TEXT (§2 below) is rejected
-- by Postgres (SQLSTATE 42804) while a uuid FK still references it — same
-- constraint that drove the actor handling above. Posture matches §4 and the
-- Slice 5 cross-aggregate rule: drop the cross-aggregate FK, keep a TEXT
-- soft-ref, rely on loud orphan rejection at LoadWorld. The §3 DELETE FROM
-- scene_huddle already fired these FKs' ON DELETE rules (SET NULL / CASCADE),
-- so the columns are all-NULL / empty here and the ::text cast can't trip the
-- hud-<hex> format.
ALTER TABLE agent_action_log DROP CONSTRAINT IF EXISTS agent_action_log_huddle_id_fkey;
ALTER TABLE agent_action_log ALTER COLUMN huddle_id TYPE TEXT USING huddle_id::text;

ALTER TABLE pay_ledger DROP CONSTRAINT IF EXISTS pay_ledger_huddle_id_fkey;
ALTER TABLE pay_ledger ALTER COLUMN huddle_id TYPE TEXT USING huddle_id::text;

ALTER TABLE scene_quote DROP CONSTRAINT IF EXISTS scene_quote_huddle_id_fkey;
ALTER TABLE scene_quote ALTER COLUMN huddle_id TYPE TEXT USING huddle_id::text;

-- §2 (design): Reshape scene_huddle.
ALTER TABLE scene_huddle DROP CONSTRAINT IF EXISTS scene_huddle_structure_id_fkey;
ALTER TABLE scene_huddle ALTER COLUMN structure_id TYPE TEXT USING structure_id::text;
ALTER TABLE scene_huddle ALTER COLUMN structure_id DROP NOT NULL;
ALTER TABLE scene_huddle ALTER COLUMN id DROP DEFAULT;
ALTER TABLE scene_huddle ALTER COLUMN id TYPE TEXT USING id::text;
ALTER TABLE scene_huddle RENAME COLUMN created_at TO started_at;

-- §6 (design): CHECK constraints on scene_huddle. The id_format CHECK
-- couples to newHuddleID's output; keep in sync.
ALTER TABLE scene_huddle ADD CONSTRAINT scene_huddle_id_nonempty
    CHECK (id <> '');
ALTER TABLE scene_huddle ADD CONSTRAINT scene_huddle_id_format
    CHECK (id ~ '^hud-[0-9a-f]{32}$');
ALTER TABLE scene_huddle ADD CONSTRAINT scene_huddle_structure_id_nonempty
    CHECK (structure_id IS NULL OR structure_id <> '');

-- Gen-marker for parent (scene_huddle).
CREATE SEQUENCE huddle_snapshot_gen_seq START 1;
ALTER TABLE scene_huddle ADD COLUMN snapshot_gen BIGINT NOT NULL DEFAULT 0;
CREATE INDEX idx_scene_huddle_snapshot_gen ON scene_huddle(snapshot_gen);

-- §5 (design): huddle_member child table + gen-marker.
CREATE TABLE huddle_member (
    huddle_id    TEXT NOT NULL REFERENCES scene_huddle(id) ON DELETE CASCADE,
    actor_id     TEXT NOT NULL,
    snapshot_gen BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (huddle_id, actor_id),
    CONSTRAINT huddle_member_huddle_id_nonempty CHECK (huddle_id <> ''),
    CONSTRAINT huddle_member_actor_id_nonempty CHECK (actor_id <> '')
);

-- UNIQUE(actor_id) enforces single-active-huddle-per-actor.
-- Concluded huddles have zero child rows (Huddle.Members wiped on
-- ConcludeHuddle), so an actor can appear at most once.
CREATE UNIQUE INDEX uniq_huddle_member_actor ON huddle_member(actor_id);
CREATE INDEX idx_huddle_member_snapshot_gen ON huddle_member(snapshot_gen);
CREATE SEQUENCE huddle_member_snapshot_gen_seq START 1;

COMMIT;
