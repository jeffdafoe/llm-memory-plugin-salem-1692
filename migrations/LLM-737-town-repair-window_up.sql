-- LLM-737: a hand's town repair survives a restart.
--
-- actor gains the in-flight town repair (town_repair_object_id /
-- town_repair_bounty / town_repair_started_at / town_repair_until). A town
-- repair runs one to two hours, so a deploy that dropped the window cost the
-- whole job; on 2026-10-09 six Tavern repairs in a row were cut by restarts.
-- ('' / 0 / NULL / NULL is the idle sentinel the checkpoint writes for "no
-- window". Only a hand's clocked town repair is written — a player's stepped
-- repair, a keeper's own mend and every eat/harvest/stoke/bake window stay
-- transient.)
--
-- ENGINE-OWNED TABLE. Apply with the engine STOPPED (stop -> migrate -> start,
-- the standard deploy order): the old binary's checkpoint does not write these
-- columns and the new binary's does.
--
-- Rerun-safe via IF NOT EXISTS.

BEGIN;

ALTER TABLE actor ADD COLUMN IF NOT EXISTS town_repair_object_id text NOT NULL DEFAULT '';
ALTER TABLE actor ADD COLUMN IF NOT EXISTS town_repair_bounty integer NOT NULL DEFAULT 0;
ALTER TABLE actor ADD COLUMN IF NOT EXISTS town_repair_started_at timestamp with time zone;
ALTER TABLE actor ADD COLUMN IF NOT EXISTS town_repair_until timestamp with time zone;

COMMIT;
