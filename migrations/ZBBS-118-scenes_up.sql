-- ZBBS-118: scenes table — per-cascade structure attribution.
--
-- A "scene" is one cascade of related ticks: a player or NPC arrives at a
-- structure, all co-located NPCs react, their replies trigger more
-- reactions, and so on until the chain quiets. The salem-engine mints
-- one UUID at the cascade origin and threads it through every chat row,
-- virtual_agent_call, and reactor tick the cascade produces — see
-- MEM-121-chat-scene-id_up.sql for the read side on chat_message_texts.
--
-- This table records each scene_id alongside the structure where the
-- cascade originated, so the admin chat UI can show "Scene · Tavern · 8
-- messages" instead of just an opaque UUID. Engine writes one row per
-- scene at mint time (see engine/scenes.go newScene helper); chat_send
-- and tick paths just thread the existing scene_id, no second write.
--
-- structure_id NULL covers cascades that aren't tied to one place:
-- chronicler dispatches for shift-boundary or phase fires, noticeboard
-- content generation, admin trigger pokes. The frontend renders no
-- location chip for those.
--
-- ON DELETE SET NULL preserves the scene record (and the chat history
-- that references its scene_id) when a structure is later removed —
-- losing location attribution is honest, losing the conversation is
-- destructive.
--
-- No FK from chat_message_texts.scene_id → scenes.scene_id: pre-existing
-- chat rows have scene_ids the engine never recorded here, and adding
-- the FK would either reject a backfill INSERT or require synthesizing
-- placeholder scenes for every historical scene_id. The admin read path
-- handles missing scenes via LEFT JOIN; the link is by-convention.

BEGIN;

CREATE TABLE scenes (
    scene_id     UUID        PRIMARY KEY,
    structure_id UUID        REFERENCES village_object(id) ON DELETE SET NULL,
    started_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Lookup by structure for "show me every scene that's happened at the
-- Tavern" admin queries. Partial — the chronicler-only / admin-trigger
-- scenes have NULL structure_id and don't need to be in the index.
CREATE INDEX ix_scenes_structure ON scenes (structure_id) WHERE structure_id IS NOT NULL;

COMMIT;
