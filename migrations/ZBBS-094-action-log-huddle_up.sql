-- ZBBS-094: agent_action_log.huddle_id — co-presence link to scene_huddle.
--
-- The sim-conversation distiller (api side) needs to assemble a per-NPC
-- daily narrative that includes other speakers' contributions when those
-- speakers were physically with the target NPC. scene_huddle (ZBBS-076)
-- already tracks per-structure conversational scoping and has the right
-- lifetime — created when the first occupant arrives, concluded when
-- the last leaves, kept after conclusion for transcript reads. The only
-- thing missing was a way to attribute each agent_action_log row back to
-- the huddle it happened in.
--
-- huddle_id is nullable because it doesn't always apply: outdoor speech
-- (no inside_structure_id → no huddle), engine-source rows like
-- object_refresh, and chore/done. Pre-migration rows are NULL.
--
-- ON DELETE SET NULL on the FK so a future scene_huddle delete (none
-- today — concluded huddles are kept — but defensive) doesn't cascade
-- away historical action log rows.
--
-- Index supports the cross-actor pull in sim_conversation_push.go:
-- "give me all speak/act rows in huddles X, Y, Z within this day window".

BEGIN;

ALTER TABLE agent_action_log
    ADD COLUMN huddle_id UUID NULL REFERENCES scene_huddle(id) ON DELETE SET NULL;

CREATE INDEX idx_agent_action_log_huddle
    ON agent_action_log(huddle_id, occurred_at)
    WHERE huddle_id IS NOT NULL;

COMMIT;
