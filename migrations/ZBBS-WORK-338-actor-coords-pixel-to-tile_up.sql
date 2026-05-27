-- ZBBS-WORK-338: convert legacy v1 actor positions from world-pixels to v2 tiles.
--
-- Maiden-boot bug. v1 stored actor.current_x/current_y as world-PIXELS (e.g.
-- 2288, 560). v2 treats Actor.Pos as a padded TILE index and the pg load
-- (engine/sim/repo/pg/actors.go LoadAll) reads current_x/current_y straight
-- into TilePos with NO unit conversion. So a v1 pixel value like 2288 loads as
-- "tile 2288" — far off the 200x180 grid — and pathfinding returns
-- "no path from (...) to target (...)": NPCs can't move.
--
-- Fix the DATA once (not the load code): this avoids touching actors.go LoadAll
-- (which HOME-317 is editing) and leaves the DB matching v2's contract that
-- current_x/current_y are tiles, so the load keeps reading them raw. Objects
-- (village_object.x/y) are CORRECTLY world-pixels in v2 (VillageObject.Pos is a
-- WorldPos) and are NOT touched. home_x/home_y are vestigial (v2 doesn't read
-- them) and are NOT touched.
--
-- Conversion is v2's own geom.go WorldPos.Tile():
--     tile = Pad + floor(pixel / TileSize)
-- with PadX=60, PadY=112, TileSize=32 (engine/sim/terrain.go).
--
-- SAFETY GUARD — only convert rows that are unambiguously world-pixels:
--   A valid v2 tile is bounded by the padded grid: tile_x in [60, 60+200-1]
--   = [60, 259], tile_y in [112, 112+180-1] = [112, 291] (PadX/Y + MapW/H).
--   So a value >= PadX+MapW (260) or >= PadY+MapH (292) CANNOT be a tile and
--   must be world-pixels. Guarding on that means this UPDATE can NEVER touch a
--   row already holding tiles (e.g. one a future v2 run wrote back, or an actor
--   created fresh via create_pc/create_npc) — no double-conversion — and the
--   migration is naturally idempotent (re-running converts nothing, since the
--   converted values are now < the thresholds).
--
--   Residual: an actor whose v1 pixel position was in the extreme top-left
--   corner of the world (both current_x < 260 AND current_y < 292, i.e. within
--   ~tile (8,9) of world origin) is indistinguishable by magnitude from a tile
--   and is left unconverted. The maiden-boot reality is almost certainly
--   all-pixels (nothing moved, no new actors, the shutdown checkpoint rewrote
--   the same pixel values it loaded), so in practice this guard converts every
--   row; the verification query in the PR lists any ambiguous-corner rows so
--   they can be hand-checked. The failure mode if one is missed is a single
--   mispositioned actor (visible at boot), never silent corruption of good data.

BEGIN;

UPDATE public.actor
   SET current_x = 60  + floor(current_x / 32.0),   -- PadX + floor(px / TileSize)
       current_y = 112 + floor(current_y / 32.0)    -- PadY + floor(py / TileSize)
 WHERE current_x >= 260   -- >= PadX + MapW: above any valid tile_x (max 259) => world-pixels
    OR current_y >= 292;  -- >= PadY + MapH: above any valid tile_y (max 291) => world-pixels

COMMIT;
