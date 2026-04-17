-- ZBBS-041: Extend village_terrain 90 rows north. Height 90 → 180.
--
-- Layout of the new map:
--   Rows 0-89   — brand new space, copy of old row 2 (carries the river's
--                 northernmost cross-section: water at col 124, grass elsewhere).
--                 Gives the impression the river flows in from the far north.
--   Rows 90-91  — old rows 0-1 with col 124 overwritten to water so the river
--                 connects into the old terrain without a visible break.
--   Rows 92-179 — old rows 2-89 unchanged.
--
-- Placed object coordinates are world-pixel coords, not grid coords, so they
-- need no update. The client-side origin offset (pad_y) shifts from 22 to 112
-- to keep world (0,0) aligned with the same tile (the original row 22).

WITH old AS (
    SELECT data FROM village_terrain WHERE id = 1
),
-- Old row 2 = bytes 400-599 (1-based positions 401-600).
row2 AS (
    SELECT substring((SELECT data FROM old) FROM 401 FOR 200) AS r
),
-- 90 copies of row 2 = the new northern territory.
prefix AS (
    SELECT decode(repeat(encode((SELECT r FROM row2), 'hex'), 90), 'hex') AS pfx
),
-- Bridge the 2-row grass gap in the old data (rows 0 and 1) by forcing col 124
-- to water. set_byte uses 0-based offsets: col 124 of row 0 = offset 124;
-- col 124 of row 1 = offset 324. Terrain type 5 = shallow water.
bridged AS (
    SELECT set_byte(set_byte((SELECT data FROM old), 124, 5), 324, 5) AS b
)
UPDATE village_terrain
SET height = 180,
    data = (SELECT pfx FROM prefix) || (SELECT b FROM bridged),
    updated_by = 'ZBBS-041',
    updated_at = NOW()
WHERE id = 1;
