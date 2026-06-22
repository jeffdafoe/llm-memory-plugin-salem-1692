-- LLM-60: name the blueberry bushes.
--
-- All 40 blueberry-asset (630909ca) village_object rows were placed / folded in
-- (LLM-58) without a display_name. The command-side loiter resolver
-- (resolveLoiteringObject, engine/sim/structure_anchors.go) skips any object with
-- DisplayName == "", so neither the gather verb (Gather/StartHarvest) nor passive
-- eat-on-arrival (ApplyObjectRefreshAtArrival) can resolve a nameless bush -- yet
-- the perception cue (findGatherableCue) + free-food list still advertise it,
-- trapping an NPC in a gather/eat loop (Prudence Ward, observed live 2026-06-22).
-- The 24 raspberry bushes (asset db4b428c) carry "Raspberry Bush" from their
-- original placement and work fine.
--
-- Fix: give every nameless blueberry-asset object the display_name "Blueberry
-- Bush", matching the raspberries. Scoped by asset and guarded on a null/empty
-- name, so a rerun is a no-op and an intentionally-renamed instance is never
-- clobbered. (The engine boot guard + umbilical /state config_warnings added in
-- LLM-60 surface any future nameless gather/eat source.)
--
-- ENGINE-OWNED TABLE. village_object is checkpoint-written by the running engine.
-- Apply with the engine STOPPED (stop -> migrate -> start) or the old binary's
-- shutdown checkpoint clobbers it. snapshot_gen is left untouched; LoadAll has no
-- gen filter, so the updated rows enter memory at boot and the first checkpoint
-- re-stamps them.

BEGIN;

UPDATE village_object
SET display_name = 'Blueberry Bush'
WHERE asset_id = '630909ca-df4f-43ac-9fc4-5192ca44da73'
  AND (display_name IS NULL OR btrim(display_name) = '');

-- Fail loud if any blueberry-asset bush is still nameless after the update -- a
-- nameless gather/eat source is exactly the LLM-60 defect. A schema-only / fresh
-- DB with zero blueberry rows passes trivially (nothing to name, count 0).
DO $$
DECLARE
    nameless_after int;
BEGIN
    SELECT count(*) INTO nameless_after
    FROM village_object
    WHERE asset_id = '630909ca-df4f-43ac-9fc4-5192ca44da73'
      AND (display_name IS NULL OR btrim(display_name) = '');
    IF nameless_after <> 0 THEN
        RAISE EXCEPTION 'LLM-60: % blueberry-asset village_object rows still have no display_name', nameless_after;
    END IF;
END $$;

COMMIT;
