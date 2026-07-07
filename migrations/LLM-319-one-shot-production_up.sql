-- LLM-319: one-shot production — `produce` required for all producers, one
-- timed cycle per call; continuous background auto-produce is retired.
--
-- Schema changes, all on engine-owned state:
--
-- 1. actor gains the in-flight production cycle (production_item /
--    production_batch_qty / production_remaining_seconds). A cycle runs tens
--    of minutes and its recipe inputs are consumed AT START, so the window
--    must survive a restart/deploy — a transient window would eat the inputs
--    on every deploy, recreating the exact coin bleed this ticket removes.
--    ('' / 0 / 0 is the idle sentinel the checkpoint writes for "no cycle".)
--
-- 2. actor.production_focus is DROPPED. The continuous "keep making it until
--    you choose again" focus (LLM-116, persisted in LLM-128) has no meaning
--    under one-shot cycles — what's being made now is production_item.
--
-- 3. actor_produce_state (+ its snapshot_gen sequence) is DROPPED. It held the
--    per-item continuous-regen anchors the auto-produce tick advanced; the
--    redesigned tick has no anchors, only the single window on the actor row.
--
-- ENGINE-OWNED TABLES. Apply with the engine STOPPED (stop -> migrate ->
-- start, the standard deploy order): the old binary's checkpoint UPSERT still
-- writes production_focus and actor_produce_state, and the new binary's writes
-- the three new columns, so neither binary may run against the other's schema.
--
-- Rerun-safe via IF EXISTS / IF NOT EXISTS.

BEGIN;

ALTER TABLE actor ADD COLUMN IF NOT EXISTS production_item text NOT NULL DEFAULT '';
ALTER TABLE actor ADD COLUMN IF NOT EXISTS production_batch_qty integer NOT NULL DEFAULT 0;
ALTER TABLE actor ADD COLUMN IF NOT EXISTS production_remaining_seconds bigint NOT NULL DEFAULT 0;

ALTER TABLE actor DROP COLUMN IF EXISTS production_focus;

DROP TABLE IF EXISTS actor_produce_state;
DROP SEQUENCE IF EXISTS actor_produce_state_snapshot_gen_seq;

COMMIT;
