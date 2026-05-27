-- ZBBS-WORK-338: convert legacy v1 actor positions from world-pixels to v2 tiles.
--
-- Maiden-boot bug (move_to "no path"). v1 stored actor.current_x/current_y as
-- world-PIXELS; v2 treats Actor.Pos as a padded TILE index and the pg load
-- (engine/sim/repo/pg/actors.go LoadAll) reads the columns straight into
-- TilePos with NO unit conversion. So a v1 pixel like 2288 loads as "tile 2288"
-- — off the 200x180 grid — and pathfinding returns "no path". NPCs can't move.
--
-- Fix the DATA once (not the load code): avoids touching actors.go LoadAll
-- (which HOME-317 edits) and leaves the DB matching v2's contract that
-- current_x/current_y are tiles, so the load keeps reading them raw. Objects
-- (village_object.x/y) are CORRECTLY world-pixels in v2 (VillageObject.Pos is a
-- WorldPos) and are NOT touched. home_x/home_y is vestigial and NOT touched.
--
-- Conversion is v2's own geom.go WorldPos.Tile():
--     tile = Pad + floor(pixel / TileSize)        PadX=60, PadY=112, TileSize=32
-- floor() is correct for NEGATIVE pixels too: the world extends into negative
-- pixel space (world (0,0) is tile (60,112), so px down to -1920 map to valid
-- tiles 0..59), e.g. floor(-1500/32) = -47 => tile 13.
--
-- UNIT DETECTION (the important part — replaces a naive magnitude guard that
-- silently skipped negative pixels). A v2 tile is a grid index bounded by
-- [0, MapW-1] = [0,199] (x) and [0, MapH-1] = [0,179] (y). The actor table is
-- written by a single engine version at a time, so if ANY row holds a
-- coordinate OUTSIDE the tile grid, the whole table is world-pixels (v1).
-- World pixels reliably produce out-of-range values for a real village (spans
-- negatives and runs to ~4480), so the uncorrelated EXISTS below is true iff
-- the table is in pixel units. When true, EVERY row matches and converts —
-- including actors whose pixel value happens to land in the ambiguous [0,199]
-- range (near world origin), which a per-row magnitude test could not classify.
-- When the table is already tiles (no out-of-range row), EXISTS is false and
-- nothing is converted: safe + idempotent, no double-conversion.

BEGIN;

UPDATE public.actor
   SET current_x = 60  + floor(current_x / 32.0),   -- PadX + floor(px / TileSize)
       current_y = 112 + floor(current_y / 32.0)    -- PadY + floor(py / TileSize)
 WHERE EXISTS (
     SELECT 1 FROM public.actor a2
      WHERE a2.current_x < 0 OR a2.current_x > 199   -- outside tile_x [0, MapW-1]
         OR a2.current_y < 0 OR a2.current_y > 179   -- outside tile_y [0, MapH-1]
 );

COMMIT;
