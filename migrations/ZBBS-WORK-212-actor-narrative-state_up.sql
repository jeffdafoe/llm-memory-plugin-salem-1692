-- ZBBS-WORK-212 — actor narrative state, Phase 1A.
--
-- The first concrete piece of an engine-side continuity layer for
-- shared-VA-backed NPCs. Persistent-VA NPCs (Ezekiel, John Ellis,
-- Prudence) get continuity from llm-memory's accumulating soul + chat
-- history; shared-VA NPCs (Hannah on salem-vendor, visitors on
-- salem-visitor) run with cache_prompts=false / dream_mode=none /
-- learning_enabled=false and have no such accumulator. Without an
-- engine-side counterpart, every tick is a blank slate and the
-- character can't have an arc.
--
-- This migration adds the simplest store: a per-actor narrative
-- backbone with two text fields.
--
--   seed_text         — author-curated character core. Set once, edited
--                       rarely. Holds plotline intent ("Hannah is a
--                       widow innkeeper whose inn quietly lets rooms by
--                       the hour for arrangements that need privacy").
--                       Injected verbatim into perception every tick so
--                       the LLM has a stable identity frame across
--                       calls.
--   evolving_summary  — engine- or distiller-written narrative state
--                       that accumulates over time. Empty in Phase 1A;
--                       populated by Phase 3 consolidation passes.
--
-- Phase 1B will add per-pair relationship state (actor_relationship)
-- so Hannah can know, per-encounter, what's happened with the speaker
-- before. Phase 2 wires event hooks (speak / pay / observe). Phase 3
-- runs periodic consolidation that compresses recent salient events
-- into evolving_summary.
--
-- Gating: the perception integration in agent_tick.go injects this
-- section only for shared-VA-backed actors (LLMMemoryAgent IN
-- ('salem-vendor', 'salem-visitor') today). VA-attached actors with
-- their own dedicated agent skip injection — they already get richer
-- context from llm-memory and another layer would over-stuff the
-- prompt. Empty rows or NULL state = no injection regardless.

BEGIN;

CREATE TABLE actor_narrative_state (
    actor_id         UUID PRIMARY KEY REFERENCES actor(id) ON DELETE CASCADE,
    seed_text        TEXT NOT NULL DEFAULT '',
    evolving_summary TEXT NOT NULL DEFAULT '',
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Seed Hannah Boggs's narrative core. Period-1692-appropriate, oblique
-- on the madame plotline (Salem talks but Hannah doesn't); explicit
-- enough that the LLM has the frame, restrained enough that it won't
-- spill the secret in casual conversation. Edit freely via
--   UPDATE actor_narrative_state
--      SET seed_text = $$<new text>$$, updated_at = NOW()
--    WHERE actor_id = (SELECT id FROM actor WHERE display_name = 'Hannah Boggs');
INSERT INTO actor_narrative_state (actor_id, seed_text)
SELECT id,
$$You are Hannah Boggs, widow of seven years, innkeeper of the Inn. The respectable face of your business is bedrooms let by the night or week. Less openly known — and never confirmed by you — the inn also keeps rooms warm for arrangements that need privacy: a married man here, a young widow there, a Boston factor passing through. You count carefully who comes, when, and on whose coin. Salem talks; Hannah does not. You are gracious to strangers who might become customers, watchful of those whose names you don't know, and quietly loyal to villagers who keep their peace with you in turn. You have no patience for fools and no fear of difficult customers — Salem is no place for a soft landlord — but pleasantries cost you nothing and may earn you a return visit, so you offer them readily.$$
FROM actor
WHERE display_name = 'Hannah Boggs'
ON CONFLICT (actor_id) DO NOTHING;

COMMIT;
