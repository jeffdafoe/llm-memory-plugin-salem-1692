-- LLM-259: persist accepted labor contracts so a restart resumes them.
--
-- The LLM-26 labor system is entirely in-memory: World.LaborLedger has no repo,
-- so every engine restart wipes it and reconcileStrandedLaboringOnLoad reverts
-- any mid-job worker to idle. Because seekWorkEligible gates on the live ledger,
-- every workless on-shift worker then re-qualifies and re-solicits on boot — the
-- whole labor market resets to a soliciting frenzy on each (frequent) deploy.
--
-- This table is the durable mirror of the ACCEPTED-but-unsettled subset of the
-- ledger — the en_route + working contracts. It is a sibling of pay_ledger: an
-- accepted contract is committed relational state (an arrangement with a reward
-- owed), not a transient queue, so it belongs in Postgres (GUIDELINES "Postgres
-- is for durable storage" — the justified case). pending (unaccepted) offers and
-- terminal (completed/declined/expired/failed) deals stay transient and are
-- never written here.
--
-- One row per accepted contract, keyed by the ledger's LaborID:
--   * labor_id           — the LaborOffer.ID; PK. bigint (LaborID is uint64).
--   * worker_id          — does the work. Soft TEXT ref to actor(id), NO FK —
--   * employer_id        — pays the reward. the v2 cross-aggregate posture
--                          (integrity enforced Go-side at LoadWorld), same as
--                          pay_ledger.buyer_id / seller_id.
--   * state              — 'en_route' | 'working'. No DB CHECK: Go owns the
--                          allowlist (SaveWorld only ever writes these two, the
--                          checkpoint filters at build time). A CHECK refusing a
--                          Go-side bug would wedge every checkpoint Tx — the
--                          actor_known_place.place_kind posture (LLM-77).
--   * reward             — the coin leg the employer pays on completion.
--   * reward_items       — the in-kind goods leg (JSONB array of {kind,qty};
--                          '[]' when coin-only). Nothing is escrowed — settle
--                          re-checks the employer holds both legs at completion.
--   * duration_min       — work-window length; needed so an en_route contract
--                          computes the right working_until when it flips to
--                          working on arrival.
--   * created_at         — solicit time (provenance/debug).
--   * accepted_at        — employer-accept time; NULL only defensively.
--   * work_started_at    — when the work window began; NULL while en_route.
--   * working_until      — completion deadline; NULL while en_route. The sweep
--                          settles a working contract at/after this.
--   * en_route_deadline  — bounded-wait deadline; NULL for an on-site working
--                          hire that started immediately (LLM-229).
--   * en_route_waiting   — true once a relocating worker reached the post and is
--                          waiting for the owner (perception phrasing).
--   * snapshot_gen       — gen-marker sync bookkeeping; matches every other
--                          checkpointed table. Standalone sequence (nextval
--                          called explicitly by SaveSnapshot, not a column
--                          default). The trailing DELETE WHERE snapshot_gen < gen
--                          prunes a contract that settled/expired between
--                          checkpoints, so the table is a true mirror of the live
--                          accepted set, not an append.
--
-- Ephemeral fields deliberately NOT persisted (they come back zero-valued on
-- load, consistent with the reactor/event-state reset): the pending-only
-- expires_at, the terminal-only resolved_at, the co-presence huddle_id/scene_id
-- (revalidated only at accept, which is past), and the causal root/source event
-- ids (the event chain is reset on load).
--
-- Engine-checkpointed standalone aggregate → deploy stop -> migrate -> start.
-- IF NOT EXISTS / guarded so a re-run (or a future re-baseline that folds this
-- into schema.sql, then replays) is a clean no-op under ON_ERROR_STOP=1.
BEGIN;

CREATE TABLE IF NOT EXISTS public.labor_contract (
    labor_id bigint NOT NULL,
    worker_id text NOT NULL,
    employer_id text NOT NULL,
    state character varying(16) NOT NULL,
    reward integer NOT NULL,
    reward_items jsonb DEFAULT '[]'::jsonb NOT NULL,
    duration_min integer NOT NULL,
    created_at timestamp with time zone NOT NULL,
    accepted_at timestamp with time zone,
    work_started_at timestamp with time zone,
    working_until timestamp with time zone,
    en_route_deadline timestamp with time zone,
    en_route_waiting boolean DEFAULT false NOT NULL,
    snapshot_gen bigint DEFAULT 0 NOT NULL,
    CONSTRAINT labor_contract_pkey PRIMARY KEY (labor_id)
);

CREATE SEQUENCE IF NOT EXISTS public.labor_contract_snapshot_gen_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

CREATE INDEX IF NOT EXISTS idx_labor_contract_snapshot_gen
    ON public.labor_contract USING btree (snapshot_gen);

COMMIT;
