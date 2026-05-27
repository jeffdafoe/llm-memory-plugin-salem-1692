-- ZBBS-WORK-338 (down): convert actor positions back from v2 tiles to v1 world-pixels.
--
-- Inverse of up, using v2's geom.go TilePos.Center():
--     pixel = (tile - Pad) * TileSize + TileSize/2     PadX=60, PadY=112, TileSize=32
-- The +16 (TileSize/2) places the pixel at the tile CENTRE — where v2 positions
-- actors — so it round-trips up exactly for tile-centred values. (up's floor()
-- already discarded any sub-tile offset, so this is a faithful inverse of what
-- up produced, not necessarily the byte-identical pre-up pixel if that pixel was
-- off-centre. Lossy unit reverse, by nature.)
--
-- Symmetric unit detection with up: convert only when the table looks like
-- TILES — i.e. NO row sits outside the tile grid [0,199]x[0,179]. After up ran,
-- every row is a valid tile, so NOT EXISTS(out-of-range) is true and all rows
-- reverse to pixels. If the table is still pixels (up never ran / already rolled
-- back), an out-of-range row exists, NOT EXISTS is false, and nothing is
-- touched — no double-reverse.

BEGIN;

UPDATE public.actor
   SET current_x = (current_x - 60)  * 32 + 16,   -- (tile_x - PadX) * TileSize + TileSize/2
       current_y = (current_y - 112) * 32 + 16    -- (tile_y - PadY) * TileSize + TileSize/2
 WHERE NOT EXISTS (
     SELECT 1 FROM public.actor a2
      WHERE a2.current_x < 0 OR a2.current_x > 199
         OR a2.current_y < 0 OR a2.current_y > 179
 );

COMMIT;
