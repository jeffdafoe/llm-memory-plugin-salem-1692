-- ZBBS-WORK-213 — actor_relationship, Phase 1B of the engine-side
-- continuity layer for shared-VA-backed NPCs.
--
-- Phase 1A (ZBBS-WORK-212) gave shared-VA actors a per-actor
-- narrative backbone. Phase 1B adds per-pair relationship state so
-- the engine can inject "what you remember of <peer>" into the
-- perception when the perceiver is in a huddle with someone whose
-- relationship row carries content. Together 1A + 1B reproduce, on
-- the salem side, the two cuts of context that llm-memory's
-- accumulating soul gives a dedicated-VA actor: who you are, and
-- who the people in front of you are to you.
--
-- Directional relationships: A's view of B is its own row, distinct
-- from B's view of A. Hannah's notes on Goodwife Smith are not the
-- same as Smith's notes on Hannah; both rows can exist independently.
-- The PRIMARY KEY (actor_id, other_actor_id) and the CHECK constraint
-- (no self-rows) enforce this.
--
-- Structure:
--   summary_text         — free-form prose synthesis. The high-level
--                          "what they are to me" frame the LLM sees
--                          first. Hand-seeded in Phase 1B; written
--                          by Phase 2 event hooks; rewritten by
--                          Phase 3 consolidation passes.
--   salient_facts JSONB  — append-only array of {at, kind, text}
--                          observations. Phase 2 writes these on
--                          speak / pay / observe events. Renderer
--                          surfaces the most recent N (3 today) so
--                          the perception stays bounded as the
--                          history grows.
--   interaction_count    — running count for stat-flavored prompts
--                          ("a customer who comes back week after
--                          week is worth more than a one-time
--                          stranger"). Updated by Phase 2 hooks.
--   last_interaction_at  — for recency-aware perception (Phase 2 +).
--
-- Existing infra interaction:
--   * `npc_acquaintance` is a binary "have we met" signal,
--     auto-populated on huddle co-presence. It stays as-is — used
--     by the "Here:" block to decide name vs. descriptor. The new
--     actor_relationship is the richer layer on top: meeting alone
--     populates the binary; salient interactions populate the
--     relationship row.
--
-- Gating: same as Phase 1A — perception injects this section only
-- for shared-VA-backed actors. VA-attached actors with their own
-- dedicated agent skip it; their llm-memory soul already covers the
-- same ground.
--
-- Phase 1B is read-only — no event hooks, no auto-writes. Rows are
-- populated by hand-authored SQL until Phase 2 lands.

BEGIN;

CREATE TABLE actor_relationship (
    actor_id            UUID NOT NULL REFERENCES actor(id) ON DELETE CASCADE,
    other_actor_id      UUID NOT NULL REFERENCES actor(id) ON DELETE CASCADE,
    summary_text        TEXT NOT NULL DEFAULT '',
    salient_facts       JSONB NOT NULL DEFAULT '[]'::jsonb,
    interaction_count   INT NOT NULL DEFAULT 0,
    last_interaction_at TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (actor_id, other_actor_id),
    CONSTRAINT actor_relationship_no_self
        CHECK (actor_id <> other_actor_id)
);

-- Reverse-direction lookup: "who has a row pointing at me" is useful
-- for cleanup and for Phase 3 / 4 features that aggregate "what others
-- think of you". Cheap, trades a small write cost for query
-- flexibility later.
CREATE INDEX idx_actor_relationship_other ON actor_relationship (other_actor_id);

-- Hand-seed example (commented out): seed Hannah's view of John
-- Ellis the tavernkeeper. Uncomment + edit when ready.
--
-- INSERT INTO actor_relationship
--     (actor_id, other_actor_id, summary_text, salient_facts, interaction_count)
-- SELECT
--     hannah.id,
--     john.id,
--     $$John Ellis runs the Tavern across the green. Steady neighbor and the only other public-house keeper in Salem; you and he have nodded over a quiet drink more than once when business was slow at both your houses. He knows nothing of the inn's quieter trade, and you intend it to stay that way.$$,
--     '[]'::jsonb,
--     0
--   FROM (SELECT id FROM actor WHERE display_name = 'Hannah Boggs') hannah,
--        (SELECT id FROM actor WHERE display_name = 'John Ellis')   john
-- ON CONFLICT (actor_id, other_actor_id) DO NOTHING;

COMMIT;
