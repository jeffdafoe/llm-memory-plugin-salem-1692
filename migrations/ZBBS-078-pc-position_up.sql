-- ZBBS-078 (M6.7 step 1): pc_position — player character presence in Salem.
--
-- A PC == a salem-realm llm-memory user. Each user playing the village
-- has one row here, keyed by their actor_name (the llm-memory user's
-- username — same string used in chat from_actor_id lookups). PCs walk
-- around the village, can enter structures, and join scene huddles
-- alongside NPCs.
--
-- actor_name as TEXT (not a typed FK) because memory_api is a separate
-- database — application layer enforces validity by always using the
-- canonical name from the user's session lookup.
--
-- current_huddle_id mirrors npc.current_huddle_id; the same scene_huddle
-- table holds both NPC and PC participants. Conclude-on-empty checks
-- need to count both tables (extension to leaveHuddle in scene_huddles
-- .go in the same commit).

BEGIN;

CREATE TABLE pc_position (
    actor_name VARCHAR(100) PRIMARY KEY,
    x DOUBLE PRECISION NOT NULL,
    y DOUBLE PRECISION NOT NULL,
    inside_structure_id UUID REFERENCES village_object(id) ON DELETE SET NULL,
    current_huddle_id UUID REFERENCES scene_huddle(id) ON DELETE SET NULL,
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_pc_position_inside ON pc_position(inside_structure_id) WHERE inside_structure_id IS NOT NULL;
CREATE INDEX idx_pc_position_huddle ON pc_position(current_huddle_id) WHERE current_huddle_id IS NOT NULL;

COMMIT;
