-- LLM-59: give Prudence Ward forage restock entries (raspberries + blueberries,
-- cap 10) so the "## Your bushes to harvest" perception cue surfaces when her
-- berry stock runs low (< 25% of cap = the global DefaultRestockReorderPct).
--
-- She owns the bushes (LLM-50 raspberry + LLM-58 blueberry farm) but produces
-- nothing (no produce entry) and was never cued to harvest them — findGatherableCue
-- is proximity-gated and her routine never passes the NW plot. The new `forage`
-- RestockSource + buildForage (engine/sim/perception/forage.go) surface her own
-- bushes, owner-only and distance-independent, gated on the same reorder
-- threshold as the buy side.
--
-- The restock policy is the union of every attribute's params.restock; the
-- `herbalist` role (currently '{}') is the natural home for her grow-to-sell
-- entries.
--
-- actor_attribute is CHECKPOINT-WRITTEN by the engine (raw params bytes written
-- back verbatim, actors.go SaveSnapshot step 30). Apply with the engine STOPPED
-- (stop -> migrate -> start), or the running binary's next checkpoint clobbers
-- this with the old '{}' it loaded at boot. On the post-migration boot the
-- engine loads the new params, derives the forage RestockPolicy, and
-- re-checkpoints them.

BEGIN;

UPDATE actor_attribute
SET params = '{"restock": [{"item": "raspberries", "source": "forage", "max": 10}, {"item": "blueberries", "source": "forage", "max": 10}]}'::jsonb
WHERE actor_id = '019dbcec-1149-7149-8a49-2cdb54680b86'  -- Prudence Ward
  AND slug = 'herbalist';

COMMIT;
