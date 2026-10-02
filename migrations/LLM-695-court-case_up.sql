-- LLM-695: the magistrates' docket and their record of rulings.
--
-- The engine does not parse conversation, so a dispute made in talk had no
-- ending: the constable pursued the theft of a ledger that never existed as an
-- item for eleven days. The constable now brings such a matter before the
-- magistrates in Salem Town (an Opus virtual agent, off the map); the court sits
-- once a day, reads the engine's records with read-only tools, and rules from a
-- closed set the engine applies. See engine/sim/court.go.
--
-- WHY DURABLE. Salem deploys several times a day. A case brought in the morning
-- and held only in memory could be lost before the noon sitting, and the
-- constable would never get his answer. The ruled rows are also the court's
-- record: the magistrate looks up his earlier rulings on the same villagers, so
-- a matter brought again is answered "the court has ruled on this". That is the
-- survive-restart and historical-retention case in shared/GUIDELINES, not a
-- queue being faked in SQL — nothing polls this table; the engine holds the
-- docket in memory and only checkpoints it.
--
-- Written by the checkpoint (SaveWorld) in the same Tx as the actors, so a pay
-- order's coin movement and the case's ruled status persist together.
-- Upsert-only; never swept.
--
-- Engine-checkpointed standalone aggregate → deploy stop -> migrate -> start.
-- IF NOT EXISTS so a re-run is a clean no-op.
BEGIN;

-- court_case — one row per matter.
--
--   * id                 — case-<8hex>, minted by the engine.
--   * filed_by_actor_id  — the villager who brought it (the constable, or whoever
--                          the operator seeding route named). Soft ref, no FK —
--                          the v2 cross-aggregate posture; a ruling must outlive
--                          the actor rows it names.
--   * parties            — [{actor_id, name}], the names as they were at filing.
--   * status             — pending until the sitting, then ruled for good.
--   * seeded             — filed by the operator (umbilical /court/file); does
--                          not count toward the filer's daily limit.
--   * result             — the closed set: no_case / found_for / pay /
--                          no_such_charge. NULL while pending.
--   * amount_ordered / amount_paid — a pay order and what the payer actually
--                          had; the rest is forgiven, no debt is carried.
--   * words              — the magistrate's ruling as he gave it.
CREATE TABLE IF NOT EXISTS public.court_case (
    id                 text        PRIMARY KEY CHECK (id ~ '^case-[0-9a-f]{8}$'),
    filed_at           timestamptz NOT NULL,
    filed_by_actor_id  text,
    filed_by_name      text        NOT NULL,
    parties            jsonb       NOT NULL CHECK (jsonb_typeof(parties) = 'array'),
    complaint          text        NOT NULL,
    status             text        NOT NULL CHECK (status IN ('pending', 'ruled')),
    seeded             boolean     NOT NULL DEFAULT false,
    ruled_at           timestamptz,
    result             text        CHECK (result IN ('no_case', 'found_for', 'pay', 'no_such_charge')),
    found_for_actor_id text,
    payer_actor_id     text,
    payee_actor_id     text,
    amount_ordered     integer     CHECK (amount_ordered IS NULL OR amount_ordered > 0),
    amount_paid        integer     CHECK (amount_paid IS NULL OR amount_paid >= 0),
    words              text,
    CHECK ((status = 'pending') = (result IS NULL)),
    CHECK ((status = 'pending') = (ruled_at IS NULL))
);

-- Crash recovery. A ruling's agent_action_log rows are written through as it is
-- given — every `ruled` row first, then the court's `paid` row — while the
-- case's ruled status and the purses reach Postgres only at the next
-- checkpoint. If the engine dies in between, the case reloads pending; before
-- the court hears a pending case it reads the rows already carrying its id,
-- finishes THAT ruling rather than asking the magistrate again (who could rule
-- differently), and writes whichever rows are missing. Partial expression
-- indexes, one per row kind, so the lookup never scans the never-trimmed log.
CREATE INDEX IF NOT EXISTS ix_agent_action_log_ruled_case_id
    ON agent_action_log ((payload->>'case_id'))
    WHERE action_type = 'ruled';
CREATE INDEX IF NOT EXISTS ix_agent_action_log_paid_court_case_id
    ON agent_action_log ((payload->>'court_case_id'))
    WHERE action_type = 'paid';

COMMIT;
