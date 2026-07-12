-- LLM-372: returning travelers + per-pair familiarity.
--
-- The transient-visitor framework (LLM-368 epic) grew a memorable-returner tier:
-- a traveler who actually dealt with a player during a visit is remembered by the
-- engine and comes back across the seasons as the SAME person, and the returner's
-- perception preface carries that continuity ("you've passed through Salem before;
-- you know Sarah Hale here"). The returner stays on the shared salem-visitor VA —
-- it is NOT promoted to a stateful zbbs-<name> agent. Identity + per-pair history
-- live durably engine-side (these two tables) and are injected as engine-authored
-- prose per call.
--
-- This IS a legitimate durable-storage case (shared/GUIDELINES "Postgres is for
-- durable storage"): a recurring_visitor row must survive engine restart AND fire
-- a return days-to-weeks out — the same category as a scheduled task, not the
-- in-memory default. Loaded into World.RecurringVisitors at boot, mutated in
-- memory, and re-persisted each checkpoint.
--
-- Unlike the in-flight `visitor` tier (LLM-369), these rows are DURABLE IDENTITY,
-- not a live mirror: they OUTLIVE the visit. So the persistence is a plain
-- upsert with NO generation-marker delete-stale sweep (the DiscoveredKind
-- precedent, not the visitor/labor_contract snapshot pattern). A recurring
-- visitor is never swept by the checkpoint; it persists until (a future) pruning
-- policy retires one that has gone unmet for a very long time.
--
-- Engine-checkpointed standalone aggregate → deploy stop -> migrate -> start.
-- IF NOT EXISTS / guarded so a re-run (or a future re-baseline that folds this
-- into schema.sql, then replays) is a clean no-op under ON_ERROR_STOP=1.
BEGIN;

-- recurring_visitor — one row per memorable returner.
--   * id             — stable rvis-<8hex> id, distinct from the per-visit
--                      vstr-<8hex> actor_id (a returner gets a fresh actor row
--                      each visit; this id is the thread that ties them together).
--   * name           — the persona's pool name ("Elias Drum"). Needed to rebuild
--                      the returner's DisplayName ("<name> the <archetype>") on a
--                      return spawn; the in-flight `visitor` tier only stores the
--                      composed DisplayName, not the bare persona name.
--   * archetype /    — the persona slots, reused verbatim on every return so the
--     origin /         same peddler-from-Boston walks back in (sprite is derived
--     disposition      from archetype via VisitorArchetypeSprite, not stored).
--   * visit_count    — how many stays this returner has made (created at 1 on the
--                      first promotion; bumped on each return spawn). Rendered as
--                      tiered prose ("you've been here once before" / "many times"),
--                      never a raw number, per scenes-not-stats.
--   * first_seen_at  — first promotion; last_seen_at — set at each departure.
--   * next_return_at — wall-clock moment this returner is due back (nullable: NULL
--                      once they have spawned and are in-village, set again at
--                      departure). The visitor spawn cascade prefers a due returner
--                      (next_return_at <= now, not currently present) over a fresh
--                      random stranger. Wall-clock to match the `visitor`
--                      ExpiresAt / boot-reconcile clock.
-- The persona CHECKs (length caps + no control characters) are defense-in-depth:
-- these fields drive both a spawned returner's DisplayName and its perception
-- prompt, so a control character or an oversized string reaching them — only
-- possible via an out-of-band edit, since normal spawn draws from closed pools —
-- could break prompt structure. The DB refuses to store such a value at all, which
-- is stronger than sanitizing on read. visit_count >= 1 pins the "created at 1,
-- only ever bumped" invariant the render tier relies on.
CREATE TABLE IF NOT EXISTS public.recurring_visitor (
    id text NOT NULL,
    name text NOT NULL,
    archetype text NOT NULL,
    origin text NOT NULL,
    disposition text NOT NULL,
    visit_count integer NOT NULL DEFAULT 1,
    first_seen_at timestamp with time zone NOT NULL,
    last_seen_at timestamp with time zone NOT NULL,
    next_return_at timestamp with time zone,
    CONSTRAINT recurring_visitor_pkey PRIMARY KEY (id),
    CONSTRAINT recurring_visitor_id_format CHECK ((id ~ '^rvis-[0-9a-f]{8}$'::text)),
    CONSTRAINT recurring_visitor_visit_count_positive CHECK (visit_count >= 1),
    CONSTRAINT recurring_visitor_name_sane CHECK (char_length(name) BETWEEN 1 AND 120 AND name !~ '[[:cntrl:]]'),
    CONSTRAINT recurring_visitor_persona_sane CHECK (
        char_length(archetype) <= 80 AND char_length(origin) <= 80 AND char_length(disposition) <= 80
        AND archetype !~ '[[:cntrl:]]' AND origin !~ '[[:cntrl:]]' AND disposition !~ '[[:cntrl:]]')
);

-- recurring_visitor_acquaintance — per (returner, PC) familiarity. The bond a
-- returner remembers toward a specific player: enough to render "you know Sarah
-- Hale here — you last saw her a few weeks back", not a staged romance arc (that
-- is a deferred follow-up). Keyed by the durable pc_actor_id; pc_display_name is
-- denormalized for render (no join to the actor aggregate at perception time).
--   * first_met_at   — first time this pair shared a scene (never re-bumped).
--   * last_met_at    — bumped on every subsequent meet; drives the recency prose.
-- ON DELETE CASCADE: a within-aggregate child->parent FK (same posture as
-- actor_need->actor), so retiring a recurring_visitor takes its familiarity with
-- it. This is the ONE FK here; the pc_actor_id is a soft ref to the actor
-- aggregate with NO FK (v2 cross-aggregate posture, Go-validated at load).
CREATE TABLE IF NOT EXISTS public.recurring_visitor_acquaintance (
    recurring_visitor_id text NOT NULL,
    pc_actor_id text NOT NULL,
    pc_display_name text NOT NULL,
    first_met_at timestamp with time zone NOT NULL,
    last_met_at timestamp with time zone NOT NULL,
    CONSTRAINT recurring_visitor_acquaintance_pkey PRIMARY KEY (recurring_visitor_id, pc_actor_id),
    CONSTRAINT recurring_visitor_acquaintance_pcname_sane CHECK (char_length(pc_display_name) <= 120 AND pc_display_name !~ '[[:cntrl:]]'),
    CONSTRAINT recurring_visitor_acquaintance_rv_fkey FOREIGN KEY (recurring_visitor_id)
        REFERENCES public.recurring_visitor (id) ON DELETE CASCADE
);

-- Links an in-flight visitor row (LLM-369 `visitor` tier) back to its durable
-- returner identity, so a mid-visit deploy — Salem deploys many times a day —
-- keeps a promoted traveler tied to their recurring_visitor row instead of (a)
-- losing the linkage and re-promoting on the next PC meet as a DUPLICATE persona,
-- or (b) a returner-spawn forgetting it is a returner. Nullable: a not-yet-
-- promoted stranger has no returner identity. Soft ref to recurring_visitor(id)
-- with NO FK (Go-validated at rehydrate, v2 cross-aggregate posture); written in
-- the SAME checkpoint Tx as the recurring_visitor row it points at, so a crash
-- can never split the link from its target.
ALTER TABLE public.visitor
    ADD COLUMN IF NOT EXISTS recurring_visitor_id text;

DO $$
BEGIN
    -- Schema-qualified existence check: match the constraint on public.visitor
    -- specifically, so a same-named constraint in another schema can't make this a
    -- false no-op. ADD CONSTRAINT has no IF NOT EXISTS, hence the guard.
    IF NOT EXISTS (
        SELECT 1
          FROM pg_constraint c
          JOIN pg_class t ON t.oid = c.conrelid
          JOIN pg_namespace n ON n.oid = t.relnamespace
         WHERE c.conname = 'visitor_recurring_visitor_id_format'
           AND n.nspname = 'public'
           AND t.relname = 'visitor'
    ) THEN
        ALTER TABLE public.visitor
            ADD CONSTRAINT visitor_recurring_visitor_id_format
            CHECK ((recurring_visitor_id IS NULL) OR (recurring_visitor_id ~ '^rvis-[0-9a-f]{8}$'::text));
    END IF;
END$$;

COMMIT;
