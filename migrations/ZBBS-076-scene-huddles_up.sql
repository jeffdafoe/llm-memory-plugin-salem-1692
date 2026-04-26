-- ZBBS-076: scene_huddle — the "who's grouped together at a location" scope.
--
-- Conversational scene = one huddle = one row in this table. Phase 1 has
-- one active huddle per structure. Phase 2 (later) adds proximity-based
-- splinter so a busy tavern can host multiple concurrent huddles.
--
-- Lifecycle:
--   - NPC enters a structure with no active huddle → create one, set
--     npc.current_huddle_id.
--   - NPC enters a structure that has an active huddle → join (set
--     current_huddle_id to the existing huddle).
--   - NPC leaves → clear their current_huddle_id. If the huddle had 0
--     participants after the leave, mark it concluded (sets concluded_at).
--
-- Concluded huddles aren't deleted — kept for transcript generation. The
-- dream-cron flow will (separately) read concluded huddles + their speech
-- timeline and write `conversations/scene-<id>` documents for memory
-- ingestion. Same shape the chat-conversations docs have, no special-
-- casing needed in the dream agent.
--
-- Why a Salem-side table instead of llm-memory-api discussions: the
-- discussion-creation path enforces a per-creator conflict guard (one
-- active discussion per agent at a time) which is fine for human-style
-- conversations but breaks the "salem-engine spawns N concurrent scenes"
-- model. Discussions stay reserved for formal events (Magistrate court
-- sessions, town meetings) where the structured participants + voting
-- semantics earn their weight. Casual ambient scenes use scene_huddle.

BEGIN;

CREATE TABLE scene_huddle (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    structure_id UUID NOT NULL REFERENCES village_object(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    concluded_at TIMESTAMPTZ
);

-- Active-huddle lookup by structure: "is there an ongoing scene at this
-- placement we should join?" Hits this index every time an NPC enters or
-- leaves a structure. Partial index keeps it small — concluded huddles
-- accumulate but are rarely queried.
CREATE INDEX idx_scene_huddle_active_structure
    ON scene_huddle(structure_id) WHERE concluded_at IS NULL;

ALTER TABLE npc
    ADD COLUMN current_huddle_id UUID REFERENCES scene_huddle(id) ON DELETE SET NULL;

CREATE INDEX idx_npc_current_huddle ON npc(current_huddle_id) WHERE current_huddle_id IS NOT NULL;

COMMIT;
