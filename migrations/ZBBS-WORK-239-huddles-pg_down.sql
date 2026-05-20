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

-- Preflight (code_review R1): make the operator-drain caveat executable.
-- This rollback retypes scene_huddle.id and scene_quote.huddle_id (PK, NOT
-- NULL) back to UUID via ::uuid casts that fail on any surviving v2
-- 'hud-<hex>' id. Without this guard the failure surfaces late (after
-- nulling agent_action_log/pay_ledger) as an opaque invalid-UUID cast error.
-- Check up front and RAISE with a clear "drain first" reason instead.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM scene_huddle
        WHERE id !~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
    ) THEN
        RAISE EXCEPTION 'Cannot roll back ZBBS-WORK-239: scene_huddle contains non-UUID v2 huddle ids; drain huddles first';
    END IF;
    IF EXISTS (
        SELECT 1 FROM scene_quote
        WHERE huddle_id !~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
    ) THEN
        RAISE EXCEPTION 'Cannot roll back ZBBS-WORK-239: scene_quote contains non-UUID huddle ids; drain quotes first';
    END IF;
END $$;

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

-- Reverse §4b: restore the additional scene_huddle.id FK referrers. id is
-- UUID again above, so the columns retype back to UUID and the FKs re-add.
-- agent_action_log/pay_ledger huddle_id is nullable — null + USING NULL
-- mirrors the actor reversal below. scene_quote.huddle_id is part of the PK
-- (NOT NULL), so it can't be nulled; the ::uuid cast requires all values be
-- UUIDs — the preflight DO-block above already aborts with a clear reason if
-- any v2 'hud-<hex>' rows survive, so this point is only reached clean.
UPDATE agent_action_log SET huddle_id = NULL WHERE huddle_id IS NOT NULL;
ALTER TABLE agent_action_log ALTER COLUMN huddle_id TYPE UUID USING NULL;
ALTER TABLE agent_action_log ADD CONSTRAINT agent_action_log_huddle_id_fkey
    FOREIGN KEY (huddle_id) REFERENCES scene_huddle(id) ON DELETE SET NULL;

UPDATE pay_ledger SET huddle_id = NULL WHERE huddle_id IS NOT NULL;
ALTER TABLE pay_ledger ALTER COLUMN huddle_id TYPE UUID USING NULL;
ALTER TABLE pay_ledger ADD CONSTRAINT pay_ledger_huddle_id_fkey
    FOREIGN KEY (huddle_id) REFERENCES scene_huddle(id) ON DELETE SET NULL;

ALTER TABLE scene_quote ALTER COLUMN huddle_id TYPE UUID USING huddle_id::uuid;
ALTER TABLE scene_quote ADD CONSTRAINT scene_quote_huddle_id_fkey
    FOREIGN KEY (huddle_id) REFERENCES scene_huddle(id) ON DELETE CASCADE;

-- Reverse actor.current_huddle_id type + FK. Up migration cleared the
-- column; explicit NULL pass here makes the precondition for the
-- USING NULL cast self-evident in this script rather than implicit
-- from up's behavior (clarity nit, code_review).
UPDATE actor SET current_huddle_id = NULL WHERE current_huddle_id IS NOT NULL;
ALTER TABLE actor ALTER COLUMN current_huddle_id TYPE UUID USING NULL;
ALTER TABLE actor ADD CONSTRAINT actor_current_huddle_id_fkey
    FOREIGN KEY (current_huddle_id) REFERENCES scene_huddle(id) ON DELETE SET NULL;

COMMIT;
