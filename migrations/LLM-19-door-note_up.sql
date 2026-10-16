-- LLM-19: notes players leave at doors nobody answered.
--
-- A player who knocks and gets no answer may pin a short note to the door for
-- one of the people of that house; the recipient reads it on their first turn at
-- or inside that house. The note must survive the deploys between the knock in
-- the morning and the recipient coming home in the evening, so it is durable:
-- the engine loads the unread rows at boot (World.DoorNotes) and upserts every
-- note each checkpoint. A read note keeps its row (read_at set) — the record of
-- what was left and read; nothing sweeps the table.
--
-- Actor and structure ids are soft refs (text, no FK), validated in Go, like
-- court_case and actor_contact. Engine-checkpointed standalone aggregate →
-- deploy stop -> migrate -> start.
BEGIN;

CREATE TABLE IF NOT EXISTS public.door_note (
    id                 text        PRIMARY KEY CHECK (id ~ '^note-[0-9a-f]{8}$'),
    structure_id       text        NOT NULL,
    recipient_actor_id text        NOT NULL,
    author_actor_id    text        NOT NULL,
    author_name        text        NOT NULL,
    body               text        NOT NULL CHECK (char_length(body) BETWEEN 1 AND 200),
    left_at            timestamptz NOT NULL,
    read_at            timestamptz
);

CREATE INDEX IF NOT EXISTS ix_door_note_unread ON public.door_note (structure_id) WHERE read_at IS NULL;

COMMIT;
