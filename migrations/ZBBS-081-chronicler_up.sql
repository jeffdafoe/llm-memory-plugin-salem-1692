-- ZBBS-081: Salem Chronicler — directorial layer for atmosphere and narrative.
--
-- Adds the world-state plumbing the salem-chronicler virtual agent needs:
-- two append-only tables for atmospheric drift and sticky narrative facts,
-- plus setting rows for mood / season / last-fired tracking and the
-- baseline-tick kill switch.
--
-- Canonical design: shared/notes/codebase/salem/overseer-design.
--
-- The chronicler fires at three phase boundaries per Salem day (dawn,
-- midday, dusk) plus at cascade origins (PC speech, NPC arrival after
-- walk). It writes atmosphere and records events; NPC perception builders
-- read the latest atmosphere + recent visible events when ticking.
--
-- Append-only design: world_environment and world_events both grow with
-- time, never overwritten. Latest row wins for "current atmosphere";
-- recent rows constitute "recent events" feed. History gives the
-- chronicler context to evolve atmosphere coherently and gives us free
-- analytics on the village's narrative arc.

BEGIN;

-- Phase enum — reusable elsewhere in the engine if anything else wants
-- to log "what phase did this happen in." Three slots per Salem day,
-- computed from existing world_dawn_time / world_dusk_time settings.
CREATE TYPE world_phase AS ENUM ('dawn', 'midday', 'dusk');

-- Event visibility scopes. Village = everyone perceives. Local = only
-- NPCs at the matching structure perceive. Private = only the named
-- NPC perceives. Most chronicler events default to village.
CREATE TYPE event_scope AS ENUM ('village', 'local', 'private');

-- Atmospheric block authored by the chronicler at each fire
-- (set_environment tool). NPC perception builders read the latest row
-- to render the "Atmosphere:" line. The chronicler reads its last
-- several rows so it can evolve atmosphere coherently rather than
-- whiplashing weather phase to phase.
CREATE TABLE world_environment (
    id      BIGSERIAL PRIMARY KEY,
    text    TEXT NOT NULL,
    set_by  TEXT NOT NULL DEFAULT 'salem-chronicler',
    phase   world_phase,  -- NULL when set outside a phase fire (cascade-origin)
    set_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX ix_world_environment_set_at ON world_environment (set_at DESC);

-- Sticky narrative facts (record_event tool). Append-only — events
-- never get "evolved" or overwritten the way atmosphere does. Filtered
-- by visibility scope when surfaced in NPC perceptions.
--
-- scope_target stores the structure_id (UUID as text) for 'local' scope
-- and the npc_id (UUID as text) for 'private' scope. NULL for village
-- scope. Loose typing — engine code interprets per scope_type. No FK
-- because events outlive deletions of structures or NPCs (a burned
-- farm should still appear in the chronicle even if the structure row
-- is removed).
CREATE TABLE world_events (
    id            BIGSERIAL PRIMARY KEY,
    text          TEXT NOT NULL,
    scope_type    event_scope NOT NULL DEFAULT 'village',
    scope_target  TEXT,
    set_by        TEXT NOT NULL DEFAULT 'salem-chronicler',
    occurred_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX ix_world_events_occurred_at ON world_events (occurred_at DESC);

-- Setting rows. The mood and season are manually flippable by an admin;
-- the chronicler reads them into its perception each fire and uses them
-- to color atmospheric writing. The last-fired pair tracks which phase
-- boundary was last processed, so we don't double-fire (and so we catch
-- up after server restart by firing only the most-recent missed phase).
-- npc_baseline_ticks_enabled is the kill switch for autonomous hourly
-- NPC ticks — off by default per the day-one design (NPCs go reactive
-- only). Flip on for safety if reactive-only feels too sparse.
INSERT INTO setting (key, value) VALUES
    ('overseer_mood', 'watchful'),
    ('salem_season', 'spring'),
    ('last_chronicler_phase_fired_at', NULL),
    ('last_chronicler_fired_phase', NULL),
    ('last_chronicler_attention_at', NULL),
    ('npc_baseline_ticks_enabled', 'false')
ON CONFLICT (key) DO NOTHING;

COMMIT;
