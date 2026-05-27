-- ZBBS-WORK-338 (down): convert actor positions back from v2 tiles to v1 world-pixels.
--
-- Inverse of the up-migration, using v2's geom.go TilePos.Center():
--     pixel = (tile - Pad) * TileSize + TileSize/2
-- with PadX=60, PadY=112, TileSize=32. The +16 (TileSize/2) places the pixel at
-- the tile CENTRE, which is where v2 positions actors anyway, so it round-trips
-- the up-migration exactly for tile-centred values (the up-migration's floor()
-- already discarded any sub-tile offset, so this is a faithful inverse of what
-- up produced — not necessarily the byte-identical original v1 pixel if that
-- pixel was off-centre).
--
-- GUARD — only convert rows in the valid tile range (current_x < 260 AND
-- current_y < 292), i.e. the rows that look like tiles. This mirrors the up
-- guard's threshold so a row left untouched by up is left untouched here, and a
-- value already in pixel range is not mangled. As with any data down-migration
-- on a lossy unit change this is best-effort reverse; it is exact for the
-- tile-centred positions v2 produces.

BEGIN;

UPDATE public.actor
   SET current_x = (current_x - 60)  * 32 + 16,   -- (tile_x - PadX) * TileSize + TileSize/2
       current_y = (current_y - 112) * 32 + 16    -- (tile_y - PadY) * TileSize + TileSize/2
 WHERE current_x < 260    -- only tile-range rows (what up produced)
   AND current_y < 292;

COMMIT;
