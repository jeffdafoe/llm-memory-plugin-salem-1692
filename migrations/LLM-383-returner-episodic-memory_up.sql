-- LLM-383: returner episodic memory — per-pair salient facts + folded summary.
--
-- LLM-372 gave a returning traveler COARSE familiarity with a player (met-before +
-- recency), rendered as the returner self-preface continuity block. This ticket
-- gives the returner real EPISODIC memory of prior visits with that player: what
-- was bought, what was discussed, how it went — so the reunion carries specifics
-- ("did that nail hold? you were fretting over the fence line last time") instead
-- of only recognition.
--
-- The mechanism reuses the persistent-NPC continuity machinery (SalientFact +
-- consolidation.go) on the returner tier, which is deliberately FIREWALLED out of
-- the actor aggregate (a returner is never an actor row — see the LLM-372/LLM-383
-- design note). So the facts live on recurring_visitor_acquaintance rather than
-- actor_relationship: three columns mirroring the actor_relationship shape.
--   * salient_facts        — the append-only {at, kind, text} trail, captured
--                            during a visit on returner<->PC speech/trade beats,
--                            keyed strictly to the pair. Bounded in Go (FIFO cap +
--                            per-fact rune truncation); the DB CHECK is a generous
--                            backstop, NOT the primary bound.
--   * summary_text         — the LLM-folded distilled impression, rewritten at
--                            visit-end from the visit's trail so a many-visit
--                            returner keeps a bounded impression, not an unbounded
--                            fact list. Between visits the trail is pruned to ~empty
--                            and only this summary carries forward.
--   * last_consolidated_at — stamped at each fold (visit-end, or a mid-visit ceiling
--                            backstop). Nullable: NULL until the first fold.
--
-- CHECK posture — deliberately GENEROUS, not tight. These columns hold LLM-generated
-- content written inside the checkpoint Tx. A tight CHECK (e.g. forbidding control
-- characters in prose) that a stray character tripped would abort the whole
-- checkpoint and wedge persistence, so the guards here are only length/shape
-- backstops that Go PROVABLY satisfies (Go caps facts far below the array bound and
-- truncates the summary far below the char bound). This mirrors actor_relationship,
-- which bounds salient_facts/summary_text in Go and carries no tight DB CHECK.
--
-- recurring_visitor_acquaintance is an engine-checkpointed durable tier → deploy
-- stops the engine, migrates, then restarts (down -> migrate -> up), so this ALTER
-- rides a normal deploy safely. IF NOT EXISTS / guarded so a re-run (or a future
-- re-baseline that replays) is a clean no-op under ON_ERROR_STOP=1. ADD COLUMN with
-- a constant DEFAULT is a fast metadata-only change (no table rewrite).
BEGIN;

ALTER TABLE public.recurring_visitor_acquaintance
    ADD COLUMN IF NOT EXISTS salient_facts jsonb NOT NULL DEFAULT '[]'::jsonb,
    ADD COLUMN IF NOT EXISTS summary_text text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS last_consolidated_at timestamp with time zone;

DO $$
BEGIN
    -- Schema-qualified existence checks: match the constraint on
    -- public.recurring_visitor_acquaintance specifically. ADD CONSTRAINT has no
    -- IF NOT EXISTS, hence the guards (LLM-372 precedent).
    IF NOT EXISTS (
        SELECT 1
          FROM pg_constraint c
          JOIN pg_class t ON t.oid = c.conrelid
          JOIN pg_namespace n ON n.oid = t.relnamespace
         WHERE c.conname = 'recurring_visitor_acquaintance_salient_facts_bounded'
           AND n.nspname = 'public'
           AND t.relname = 'recurring_visitor_acquaintance'
    ) THEN
        ALTER TABLE public.recurring_visitor_acquaintance
            ADD CONSTRAINT recurring_visitor_acquaintance_salient_facts_bounded
            CHECK (jsonb_typeof(salient_facts) = 'array' AND jsonb_array_length(salient_facts) <= 200);
    END IF;

    IF NOT EXISTS (
        SELECT 1
          FROM pg_constraint c
          JOIN pg_class t ON t.oid = c.conrelid
          JOIN pg_namespace n ON n.oid = t.relnamespace
         WHERE c.conname = 'recurring_visitor_acquaintance_summary_sane'
           AND n.nspname = 'public'
           AND t.relname = 'recurring_visitor_acquaintance'
    ) THEN
        ALTER TABLE public.recurring_visitor_acquaintance
            ADD CONSTRAINT recurring_visitor_acquaintance_summary_sane
            CHECK (char_length(summary_text) <= 4000);
    END IF;
END$$;

COMMIT;
