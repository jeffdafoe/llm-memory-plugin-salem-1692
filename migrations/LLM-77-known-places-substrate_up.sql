-- LLM-77 (epic LLM-76, World-memory Half A — foundation): durable per-actor
-- "known places/sources" substrate.
--
-- An NPC's perception is rebuilt from the snapshot every tick, so it knows only
-- what it is shown right now — it has no record of the places/sources it has
-- been to, used, or owns. This table is the durable, per-actor memory of WHICH
-- places an actor knows and WHAT each is good for (its affordances). It is the
-- permanent half of world-memory (a location doesn't move; you don't un-know
-- your own farm), so it lives in Postgres at the same durability tier as
-- actor_relationship.salient_facts — survives restart, loaded into every
-- perception build.
--
-- This migration ships the storage only. No resolver/renderer reads it yet
-- (navigation is LLM-78, cues are LLM-79), so it is inert and safe: rows
-- accumulate, nothing consumes them.
--
-- One row per (actor, place). It mirrors the actor_relationship tier exactly:
--   * actor_id     — owning actor; FK to actor(id) ON DELETE CASCADE.
--   * place_ref    — the move_to handle: a village_object id, or a structure id
--                    (structures share their id with their village_object
--                    placement). Soft ref — TEXT-tier uuid, NO FK to
--                    village_object/structure (a real FK would force a cross-
--                    aggregate retype; integrity is enforced at LoadWorld, the
--                    same posture every other cross-aggregate ref uses).
--   * place_kind   — 'object' | 'structure' discriminator. No DB CHECK: Go owns
--                    the allowlist (the v2 "schema is a dumb mirror, sim trusts
--                    its in-process invariants" posture — a CHECK that refused a
--                    Go-side bug would wedge every checkpoint Tx). Load + Save
--                    validate the value symmetrically in the repo.
--   * affordances  — JSONB capability array, e.g. ["gather:raspberries",
--                    "vendor:bread", "own_anchor:farm"] (capabilities as a JSON
--                    array, not a pile of boolean columns).
--   * first_learned_at / last_experienced_at — provenance; written verbatim from
--                    the in-memory values by the repo (same as relationship
--                    created_at/updated_at).
--   * snapshot_gen — gen-marker sync bookkeeping; matches the actor aggregate's
--                    other tiers. Standalone sequence (nextval called explicitly
--                    by SaveSnapshot, not a column default).
--
-- Engine-checkpointed actor-aggregate table → deploy stop -> migrate -> start.
-- IF NOT EXISTS / guarded so a fresh DB (schema.sql already created this, then
-- this replays) is a clean no-op under ON_ERROR_STOP=1.
BEGIN;

CREATE TABLE IF NOT EXISTS public.actor_known_place (
    actor_id uuid NOT NULL,
    place_ref uuid NOT NULL,
    place_kind text NOT NULL,
    affordances jsonb DEFAULT '[]'::jsonb NOT NULL,
    first_learned_at timestamp with time zone DEFAULT now() NOT NULL,
    last_experienced_at timestamp with time zone DEFAULT now() NOT NULL,
    snapshot_gen bigint DEFAULT 0 NOT NULL,
    CONSTRAINT actor_known_place_pkey PRIMARY KEY (actor_id, place_ref)
);

CREATE SEQUENCE IF NOT EXISTS public.actor_known_place_snapshot_gen_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

CREATE INDEX IF NOT EXISTS idx_actor_known_place_snapshot_gen
    ON public.actor_known_place USING btree (snapshot_gen);

-- ADD CONSTRAINT has no IF NOT EXISTS; guard on pg_constraint so a re-run is a
-- clean no-op.
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint c
        JOIN pg_class t ON t.oid = c.conrelid
        JOIN pg_namespace n ON n.oid = t.relnamespace
        WHERE c.conname = 'actor_known_place_actor_id_fkey'
          AND n.nspname = 'public'
          AND t.relname = 'actor_known_place'
    ) THEN
        ALTER TABLE ONLY public.actor_known_place
            ADD CONSTRAINT actor_known_place_actor_id_fkey
            FOREIGN KEY (actor_id) REFERENCES public.actor(id) ON DELETE CASCADE;
    END IF;
END $$;

COMMIT;
