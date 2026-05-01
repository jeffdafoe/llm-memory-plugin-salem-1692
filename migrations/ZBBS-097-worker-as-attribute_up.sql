-- ZBBS-097: bring worker into the attribute system.
--
-- ZBBS-096 deliberately excluded 'worker' from attribute_definition on the
-- (incorrect) read that worker had no engine dispatch. It does — the
-- per-NPC worker scheduler in engine/npc_scheduler.go reads
-- actor.behavior = 'worker' to find workers and runs scheduled go-to-work
-- / go-home moves for them. Leaving worker out of the attribute system
-- created a UI inconsistency (handleListNPCs still showed "behavior:
-- worker" for legacy rows but handleListNPCBehaviors no longer offered
-- worker as a selectable option) and prevented the actor.behavior column
-- from retiring.
--
-- This migration:
--
--   1. Adds the worker attribute_definition row. behaviors[] is empty —
--      worker scheduling is per-NPC scheduled state, not a route-walking
--      spec, and lives in the scheduler rather than as a behavior_handler.
--      The attribute exists so admin UIs can find/assign/display it
--      uniformly with the other roles.
--
--   2. Migrates the existing actor.behavior = 'worker' rows into
--      actor_attribute. After this, every dispatchable role lives in
--      actor_attribute and actor.behavior is no longer read by engine
--      code (the scheduler switches in the same commit).
--
-- The actor.behavior column itself stays alive as orphan data through
-- this migration; a follow-up retires it after a stability window.

BEGIN;

INSERT INTO attribute_definition (slug, display_name, description, behaviors) VALUES (
    'worker',
    'Worker',
    'General villager who commutes to their assigned work structure on a schedule and returns home after their shift. Worker scheduling lives in npc_scheduler.go and reads actor.work_structure_id together with the schedule_*_minute / lateness_window_minutes columns directly; the attribute carries no behavior specs of its own.',
    '[]'::jsonb
);

-- Move the existing worker rows into actor_attribute. Idempotent against
-- a partial reapply: ON CONFLICT DO NOTHING in case any worker was
-- separately assigned the worker attribute between deploy and this
-- migration running (defensive — not expected today).
INSERT INTO actor_attribute (actor_id, slug)
SELECT id, behavior FROM actor
WHERE behavior = 'worker'
ON CONFLICT (actor_id, slug) DO NOTHING;

COMMIT;
