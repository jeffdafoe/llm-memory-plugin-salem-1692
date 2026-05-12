-- ZBBS-HOME-274 — split client-liveness from action-recency for PC sweeps.
--
-- The pc-presence-cleanup sweep (engine/pc_presence_sweep.go, ZBBS-HOME-267)
-- was originally gated on actor.last_pc_input_at, the timestamp of the
-- player's last explicit HTTP action (move, say, speak, pay, ...). That
-- column has two other consumers — pc_idle_sleep_minutes auto-bed
-- (engine/sleep.go) and sleeping_until input-wake — both of which
-- legitimately want "did the player just act?" semantics.
--
-- Presence-cleanup wants something different: "is this PC's client
-- still connected?" A PC who is mid-dwell (eating stew for 16 min,
-- resting at a tree, sitting through a long NPC reply) is alive and
-- engaged but takes no input. The old column conflated those, so the
-- sweep was firing on actively-playing PCs after 5 minutes of dwell —
-- yanking inside_structure_id + current_huddle_id mid-conversation,
-- triggering engine-authored farewell lines from the NPC they were
-- still talking to. Observed 2026-05-12 22:30 UTC: Jefferey eating
-- stew at the Tavern, cleared mid-meal, John Ellis emitted "Until
-- next time, Jefferey. The hearth's always warm."
--
-- Fix: add a separate last_pc_seen_at column whose source is client
-- liveness (the /pc/me poll fires every 10s from the talk panel for
-- the lifetime of the open client, independent of player action).
-- The presence sweep moves to last_pc_seen_at; last_pc_input_at stays
-- as-is for auto-bed and input-wake. Two columns, two clean semantics,
-- no overload.
--
-- Backfill: COALESCE(last_pc_input_at, NOW()) for existing PCs so the
-- migration preserves prior sweep behavior on deploy — a PC who was
-- already a ghost under the old column stays a ghost candidate under
-- the new one, and a PC who never acted gets a fresh-now baseline so
-- a freshly-seeded actor isn't classified as stale on day one.

BEGIN;

ALTER TABLE actor
    ADD COLUMN IF NOT EXISTS last_pc_seen_at TIMESTAMPTZ;

UPDATE actor
   SET last_pc_seen_at = COALESCE(last_pc_input_at, NOW())
 WHERE login_username IS NOT NULL
   AND last_pc_seen_at IS NULL;

COMMIT;
