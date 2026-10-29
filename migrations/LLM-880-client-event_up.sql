-- LLM-880: client-only events the Godot client reports, kept for statistics.
--
-- Some effects run only in the client (the night sounds, the figure at the
-- treeline), so the engine never sees them. The client posts each one to
-- POST /api/village/client-event; the engine stamps the time, the authed
-- username and the caller's PC, and writes the row straight to this table (not
-- world state, no checkpoint path). Operators read counts and recent rows at
-- GET /umbilical/client-events. Untrusted, client-claimed data: nothing in the
-- engine reads it to make a decision.
--
-- The engine restarts many times a day, so an in-memory ring (like the client
-- error feed) would never count an event that happens a few times a season.
-- actor_id is a soft ref (text, no FK), like door_note. Nothing prunes the
-- table; the volume is tiny. Additive: apply before the engine that writes it.
BEGIN;

CREATE TABLE IF NOT EXISTS public.client_event (
    id       bigserial   PRIMARY KEY,
    at       timestamptz NOT NULL,
    username text        NOT NULL,
    actor_id text,
    kind     text        NOT NULL,
    detail   jsonb       NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(detail) = 'object')
);

CREATE INDEX IF NOT EXISTS ix_client_event_kind_at ON public.client_event (kind, at);
CREATE INDEX IF NOT EXISTS ix_client_event_at ON public.client_event (at);

COMMIT;
