-- LLM-547: per-pair conversational recency.
--
-- Perception has always told an actor what its round has LEFT and never who it
-- has already SPOKEN WITH. Constable Marsh opened a fresh conversation with
-- Prudence Ward three times inside one round; she was the one who noticed
-- ("Three times is thrice too many"). The engine knew every one of those calls
-- and rendered none of them.
--
-- This table is the durable half of the fix. The live record is in-memory
-- (World.ContactLedger, engine/sim/contact_ledger.go) and every read and write
-- goes there; these rows exist only so the trail survives a restart.
--
-- WHY DURABLE AT ALL. The original design was restart-lossy. LLM-546 reversed
-- it: an innkeeper greeted a player as a stranger six hours after selling him
-- porridge, with three deploys in between. Salem deploys several times a day, so
-- restart-lossy would leave the hours-scale continuity tier empty for most of a
-- working day — and it is a human player, who remembers perfectly across our
-- restarts, who notices. This is the legitimate durable case per shared/
-- GUIDELINES ("Postgres is for durable storage"): the requirement is literally
-- survive-restart. It is NOT a queue, a timer, or a scheduler being faked in
-- SQL — nothing polls this table and no process but the engine reads it.
--
-- Written by the checkpoint (SaveWorld), so a crash loses at most the last
-- minute of contacts. That is the correct side of the consistency line: the
-- trail is soft conversational memory, not coin or stock, and a missing entry
-- degrades to "no line rendered" rather than to an inconsistency.
--
-- Persistence posture is RecurringVisitors', not the visitor mirror's: a plain
-- upsert with NO generation-marker delete-stale sweep. A pair that stops being
-- written simply ages past the recall horizon and is dropped at the next load
-- (RehydrateContactLedger), so there is nothing for a sweep to reclaim and no
-- safety cron to justify.
--
-- Engine-checkpointed standalone aggregate → deploy stop -> migrate -> start.
-- IF NOT EXISTS / guarded so a re-run (or a future re-baseline that folds this
-- into schema.sql, then replays) is a clean no-op under ON_ERROR_STOP=1.
BEGIN;

-- actor_contact — one row per ORDERED pair (subject, peer).
--
--   * actor_id / peer_id — ordered, not canonicalized. Both directions are
--                          written independently on every speak, so the pair
--                          (A,B) and (B,A) each get a row. They will normally
--                          agree; keeping them separate means a read never has
--                          to canonicalize a key, and a future asymmetric fact
--                          (one party remembers, the other does not) has a
--                          place to live without a migration.
--   * contact_at         — the contact trail as a timestamptz array, oldest
--                          first, capped engine-side at MaxContactsPerPair.
--                          An ARRAY rather than a row-per-contact because the
--                          whole trail is read, written and pruned as ONE unit
--                          and is never queried by element; a child table would
--                          buy nothing and cost a join plus a sweep. Bounded by
--                          the same cap, so the array cannot grow without limit.
--
-- The trail rather than a bare last_spoke_at because the tiers COUNT: "twice
-- already this round" is a different line from "already this round", and that
-- distinction is the one that carries the peer's state rather than only the
-- subject's history.
--
-- NO FOREIGN KEY on either id. This is the v2 cross-aggregate posture that
-- recurring_visitor_acquaintance.pc_actor_id already uses — soft refs, validated
-- in Go at load. It is load-bearing here rather than merely conventional: a
-- transient visitor's actor row is DELETED at cleanup, and this ledger covers
-- visitors deliberately (a traveller working a circuit of shops is exactly the
-- actor a route-scoped design would miss). An FK would either block that delete
-- or cascade away a trail we are content to let age out. Orphan rows are
-- harmless: every read is keyed by a co-present peer's id, and an actor who no
-- longer exists is never co-present.
--
-- No index beyond the primary key. The engine reads this table exactly once, at
-- boot, with a full scan into memory; the PK covers the upsert. Row count is
-- bounded by actor pairs — tens of actors, so hundreds of rows.
CREATE TABLE IF NOT EXISTS public.actor_contact (
    actor_id text NOT NULL,
    peer_id text NOT NULL,
    contact_at timestamp with time zone[] NOT NULL DEFAULT '{}',
    CONSTRAINT actor_contact_pkey PRIMARY KEY (actor_id, peer_id),
    -- A self-pair is meaningless and the engine drops it at both write and
    -- rehydrate; refuse to store one at all so an out-of-band insert cannot
    -- create a record claiming an actor spoke with themselves.
    CONSTRAINT actor_contact_not_self CHECK (actor_id <> peer_id),
    -- Defense-in-depth against an out-of-band write: the engine caps the trail
    -- at MaxContactsPerPair (8) on every write and rehydrate. Allow headroom
    -- over that constant so raising the cap does not require a migration, while
    -- still refusing an unbounded array.
    CONSTRAINT actor_contact_trail_bounded CHECK (array_length(contact_at, 1) IS NULL OR array_length(contact_at, 1) <= 64)
);

COMMIT;
