-- LLM-262: tower assets are unenterable, so a tower-backed workplace (the Mill)
-- traps its keeper outside — the shift-duty cue reads "away from your post" forever
-- and the keeper never produces.
--
-- Enterability is gated purely on door_offset: structureEntryTile
-- (engine/sim/structure_anchors.go) returns "cannot be entered" when door_offset is
-- NULL, so an actor can only loiter at the outdoor pin, InsideStructureID never
-- flips, and "at post" (build.go: InsideStructureID == work_structure_id) is never
-- true. The `interior` column is vestigial — the engine's asset load never reads it,
-- and every working enterable structure (houses, market stalls, the Blue Castle) has
-- interior = false.
--
-- Set the door to the bottom-center footprint tile (0, 0). Towers have
-- FootprintBottom = 0, so door_offset_y must be <= 0 to fall inside the footprint;
-- (0, 0) is the bottom-center/base tile, matching the enterable Blue Castle (0,0) and
-- Outhouse (0,0). (0, +1) would sit one tile BELOW the footprint and could never
-- satisfy structureContainingTile.
--
-- `asset` is boot-loaded reference data (not checkpoint-written by the running
-- engine), so no engine stop is required and this cannot be clobbered by a
-- checkpoint; the change takes effect on the next restart (deploy stop -> migrate ->
-- restart). Matches by stable seed name and is value-idempotent (rerun re-sets the
-- same 4 rows); the ROW_COUNT = 4 guard makes seed drift (a renamed / removed / added
-- tower asset) fail loudly instead of silently updating 0, 3, or 5 rows.

BEGIN;

DO $$
DECLARE
    updated_count integer;
BEGIN
    UPDATE asset
    SET door_offset_x = 0,
        door_offset_y = 0
    WHERE name IN ('Black Tower', 'Blue Tower', 'Red Tower', 'Yellow Tower');

    GET DIAGNOSTICS updated_count = ROW_COUNT;

    -- Tolerate 0 (LLM-265): the schema-only fresh-replay path (the pg integration
    -- harness loads schema.sql then replays every *_up.sql) has no seeded tower
    -- `asset` rows, so this UPDATE matches 0 and the strict `<> 4` guard aborted the
    -- whole template build — reddening CI for every salem PR. A seeded DB (prod, or a
    -- data-loaded fixture) still fails loudly on partial seed drift (1/2/3/5). Prod
    -- already applied this at 4 rows, so admitting 0 is outcome-equivalent there.
    IF updated_count NOT IN (0, 4) THEN
        RAISE EXCEPTION 'LLM-262: expected to set door_offset on 0 (schema-only) or 4 tower assets, updated %', updated_count;
    END IF;
END $$;

COMMIT;
