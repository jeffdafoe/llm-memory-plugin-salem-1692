-- ZBBS-041 down: shrink the terrain back to 200x90 by dropping the first 90
-- rows (the new northern space). Leaves the bridge bytes at old rows 0-1
-- (now at the top of the shrunk map) in place — they're valid terrain, just
-- with col 124 forced to water, which is cosmetically harmless.

UPDATE village_terrain
SET height = 90,
    data = substring(data FROM (90 * 200 + 1) FOR (90 * 200))
WHERE id = 1;
