-- LLM-369: persist in-flight travelers (transient visitors) so a restart
-- resumes them instead of vanishing them mid-scene.
--
-- The transient-visitor framework (engine/sim/visitor.go + engine/sim/cascade/
-- visitor.go) is live but explicitly NOT persisted: engine/sim/repo/pg/actors.go
-- SaveSnapshot filters out every actor with VisitorState != nil, on the original
-- "transient by design" assumption that restart-loss was acceptable. It isn't —
-- Salem is deployed constantly, so a traveler evaporates mid-conversation on
-- every deploy. This table is the durable mirror of the in-flight visitor set,
-- and the reload path the actors.go comment said didn't exist.
--
-- It is a sibling checkpoint tier, modeled on labor_contract (LLM-259): written
-- inside the SaveWorld Tx from the visitor subset of CheckpointSnapshot.Actors,
-- and rehydrated in World.FinalizeLoad. Visitors stay OUT of the 11-tier actor
-- aggregate (their social tiers — relationships / narrative / acquaintance /
-- consolidation — are firewalled off them), so they get this lean tier of their
-- own. All typed columns, matching labor_contract; there is no nested/list data
-- yet — the day-plan pack + itinerary (LLM-373) land as jsonb then, exactly the
-- way labor_contract carries reward_items.
--
-- One row per in-flight visitor, keyed by the vstr-<8hex> ActorID:
--   * actor_id            — the visitor's ActorID; PK. Format-checked.
--   * display_name        — "<Name> the <archetype>", the actor's display name.
--   * archetype           — persona slots (peddler / traveling scholar / ...),
--   * origin                the outside town, and the disposition — they feed the
--   * disposition           perception identity preface + "Visitors here" cue.
--   * position_x/y        — last-checkpointed tile (padded-tile coords, same
--                           convention as the actor aggregate). A re-issued
--                           arrival walk re-plans the path from here.
--   * inside_structure_id — soft TEXT ref to structure(id), NO FK (Go-side
--                           validated at rehydrate, same posture as the actor
--                           aggregate's cross-refs). NULL when outdoors.
--   * expires_at          — wall-clock departure deadline. The boot reconcile
--                           drops a visitor whose stay elapsed while down.
--   * phase               — visitor lifecycle state, a Go-owned string enum. NO
--                           DB CHECK (Go owns the allowlist; a CHECK refusing a
--                           Go-side value would wedge the checkpoint Tx, matching
--                           labor_contract.state). Today 'present' | 'departing';
--                           LLM-373 adds 'arriving' / 'making_rounds' / 'lodging'.
--   * snapshot_gen        — gen-marker sync bookkeeping, same as every other
--                           checkpointed tier. Standalone sequence; the trailing
--                           DELETE WHERE snapshot_gen < gen prunes visitors that
--                           departed / were cleaned up between checkpoints, so the
--                           table is a true mirror of the live in-flight set.
--
-- Deliberately NOT persisted: visitor need-rows (reseeded to 0 on rehydrate —
-- visitors have no idle-backstop and their lifecycle is ExpiresAt-driven,
-- matching what spawn does today).
--
-- Engine-checkpointed standalone aggregate → deploy stop -> migrate -> start.
-- IF NOT EXISTS / guarded so a re-run (or a future re-baseline that folds this
-- into schema.sql, then replays) is a clean no-op under ON_ERROR_STOP=1.
BEGIN;

CREATE TABLE IF NOT EXISTS public.visitor (
    actor_id text NOT NULL,
    display_name text NOT NULL,
    archetype text NOT NULL,
    origin text NOT NULL,
    disposition text NOT NULL,
    position_x integer NOT NULL,
    position_y integer NOT NULL,
    inside_structure_id text,
    expires_at timestamp with time zone NOT NULL,
    phase text NOT NULL,
    snapshot_gen bigint DEFAULT 0 NOT NULL,
    CONSTRAINT visitor_pkey PRIMARY KEY (actor_id),
    CONSTRAINT visitor_actor_id_format CHECK ((actor_id ~ '^vstr-[0-9a-f]{8}$'::text)),
    CONSTRAINT visitor_inside_structure_id_nonempty CHECK (((inside_structure_id IS NULL) OR (btrim(inside_structure_id) <> ''::text)))
);

CREATE SEQUENCE IF NOT EXISTS public.visitor_snapshot_gen_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

CREATE INDEX IF NOT EXISTS idx_visitor_snapshot_gen
    ON public.visitor USING btree (snapshot_gen);

COMMIT;
