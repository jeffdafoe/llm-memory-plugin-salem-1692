-- ZBBS-073: agent action audit log (M6.2).
--
-- Records every action an LLM-driven agent takes in the world. Written
-- by the engine when it RESOLVES a tool call returned from
-- llm-memory-api's /agent/tick endpoint. The LLM's request to do
-- something is not logged here — only the engine's resolution of it.
--
-- Source distinguishes who issued the action: 'agent' (a per-NPC VA),
-- 'magistrate' (the Elder coordinator with extra powers), or 'player'
-- (player-issued actions through the UI like accuse/petition).
--
-- Result is the engine's verdict: 'ok' (action took effect), 'rejected'
-- (refused before acting — e.g. accuse target you've never met), or
-- 'failed' (started acting then errored — e.g. pathfind fail mid-walk).
-- Error is human-readable detail when result != 'ok'.
--
-- Payload is the action-type-specific parameters: { "destination": "tavern" }
-- for walk_to, { "text": "...", "target": "<npc_id>" } for speak, etc.
--
-- M6.2 lands the table only. The engine starts writing rows in M6.3
-- when it begins resolving tool calls returned from /agent/tick.

BEGIN;

CREATE TABLE agent_action_log (
    id          BIGSERIAL PRIMARY KEY,
    npc_id      UUID NOT NULL REFERENCES npc(id) ON DELETE CASCADE,
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    source      TEXT NOT NULL,
    action_type TEXT NOT NULL,
    payload     JSONB NOT NULL,
    result      TEXT NOT NULL,
    error       TEXT,
    CONSTRAINT agent_action_log_source_check
        CHECK (source IN ('agent', 'magistrate', 'player')),
    CONSTRAINT agent_action_log_result_check
        CHECK (result IN ('ok', 'rejected', 'failed'))
);

-- Per-NPC reverse-chronological lookup powers the perception builder's
-- "recent actions by you" block — M6.4 needs the last few rows for an NPC.
CREATE INDEX idx_agent_action_log_npc ON agent_action_log(npc_id, occurred_at DESC);

COMMIT;
