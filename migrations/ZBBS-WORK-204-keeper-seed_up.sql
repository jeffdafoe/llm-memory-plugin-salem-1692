-- ZBBS-WORK-204 (commit 1 of 2): drop UNIQUE on actor.llm_memory_agent +
-- seed the inn keeper Hannah Boggs.
--
-- This is the schema-and-keeper-only piece. The lodger-side migration
-- (clear home_structure_id for boarders, seed initial nights_stay rows
-- for Ezekiel + PCs) ships in commit 2 alongside the engine code that
-- backstops them (auto-rebook sweep, lodger perception cues, NPC-lodger
-- sleep fallback). Splitting lets the keeper be visually verified in
-- the running engine before flipping the boarder semantics.

BEGIN;

-- 1. Drop the UNIQUE constraint on actor.llm_memory_agent.
--
-- The original ZBBS-084 schema enforced one-actor-per-VA, which made
-- sense when every agentized NPC had a bespoke VA. The shared-VA
-- pattern (salem-visitor for transient strangers, salem-vendor for
-- keepers / shopkeepers) requires multiple actors to share an agent
-- name; per-actor identity comes from engine-injected per-call context
-- (persona preface in buildAgentPerception) rather than from the VA's
-- own chat history or learnings.
--
-- Practical impact pre-drop: visitor_max_concurrent was silently
-- capped at 1 (any 2nd visitor's INSERT failed the unique check, no
-- user-visible effect besides a log line). With this drop the cap
-- operates as designed, and salem-vendor can back any number of
-- keepers concurrently.
--
-- Replace with a non-unique index for query performance — the engine
-- queries by llm_memory_agent on the idle-sweep + agent-tick paths.
ALTER TABLE actor DROP CONSTRAINT actor_llm_memory_agent_key;
CREATE INDEX idx_actor_llm_memory_agent ON actor (llm_memory_agent)
    WHERE llm_memory_agent IS NOT NULL;

-- 2. Default weekly rent rate. Engine-auto rebook (commit 2) charges
-- this when the LLM-driven renewal conversation doesn't fire in time.
-- Operator can tune via UPDATE setting once we have observation data.
INSERT INTO setting (key, value) VALUES ('lodging_default_weekly_rate', '28')
ON CONFLICT (key) DO NOTHING;

-- 3. Seed Hannah Boggs as the inn keeper.
--
-- Decorative-villager-style actor backed by salem-vendor for
-- transactional behavior. Old Woman A (v00) sprite — older Puritan-
-- coded for an innkeeper role.
--
-- The inn structure is identified as the village_object that's tagged
-- 'lodging' but NOT 'tavern' — ZBBS-180 auto-tags taverns as
-- 'lodging' too, so the !'tavern' filter isolates the pure inn.
--
-- inside=true + inside_structure_id=inn places her physically inside
-- on creation so customers walking in see her without waiting for a
-- scheduler tick. work_structure_id=inn ties her as the seller for
-- nights_stay bookings; home_structure_id=inn gives her a canEnter
-- exemption (she lives at her workplace). The actor row's coins
-- defaults to 20 (ZBBS-084), which is fine for a keeper — she earns
-- from rent rather than spending.
INSERT INTO actor (
    display_name, sprite_id,
    current_x, current_y, facing,
    inside, inside_structure_id,
    home_structure_id, work_structure_id,
    llm_memory_agent
)
SELECT
    'Hannah Boggs',
    sp.id,
    inn.x, inn.y, 'south',
    true, inn.id,
    inn.id, inn.id,
    'salem-vendor'
FROM (
    SELECT o.id, o.x, o.y FROM village_object o
    WHERE o.id IN (SELECT object_id FROM village_object_tag WHERE tag = 'lodging')
      AND o.id NOT IN (SELECT object_id FROM village_object_tag WHERE tag = 'tavern')
    LIMIT 1
) inn,
npc_sprite sp
WHERE sp.name = 'Old Woman A (v00)';

-- 4. Seed Hannah's actor_inventory with nights_stay x1.
--
-- Sentinel row — service items don't decrement, but the inventory row
-- gates the keeper's "Items you can sell" perception line and lets
-- the LLM use mentions=["nights_stay"] in speak.
INSERT INTO actor_inventory (actor_id, item_kind, quantity)
SELECT id, 'nights_stay', 1
FROM actor WHERE display_name = 'Hannah Boggs';

COMMIT;
