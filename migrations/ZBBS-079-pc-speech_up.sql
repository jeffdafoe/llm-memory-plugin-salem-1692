-- ZBBS-079 (M6.7 step 3): PC speech in agent_action_log.
--
-- PCs speak to the room and to specific NPCs. Both forms need to land
-- in the audit log so the perception's "Recent:" block surfaces what
-- they said to co-located NPCs (same path NPC speech takes, M6.4.6).
--
-- Existing rows have npc_id (UUID FK) for the speaker. PCs aren't NPCs
-- and have no npc row, so npc_id becomes nullable; speaker_name (TEXT)
-- holds the canonical display name for both kinds — npc.display_name
-- for NPC rows, pc_position.actor_name for PC rows.
--
-- Backfill: speaker_name populated from existing npc rows on UP
-- migration so the recent-block query can drop the JOIN to npc and
-- read speaker_name directly.

BEGIN;

ALTER TABLE agent_action_log
    ADD COLUMN speaker_name VARCHAR(100);

UPDATE agent_action_log a
SET speaker_name = n.display_name
FROM npc n
WHERE n.id = a.npc_id;

ALTER TABLE agent_action_log
    ALTER COLUMN npc_id DROP NOT NULL,
    ALTER COLUMN speaker_name SET NOT NULL;

-- Source vocabulary expands: 'agent' (existing NPC tick), 'scheduler'
-- (background scheduler), 'pc' (player). No CHECK constraint on
-- source — keeps it open for future extensions.

COMMIT;
