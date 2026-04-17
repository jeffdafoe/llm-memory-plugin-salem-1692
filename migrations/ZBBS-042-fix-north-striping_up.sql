-- ZBBS-042: Fix the vertical striping in the ZBBS-041 northern extension.
--
-- ZBBS-041 replicated old row 2 (a speckled mix of light/dark grass with one
-- water tile at col 124) 90 times. Because every new row had the identical
-- x-pattern, every vertical column got a single fixed terrain type across
-- all 90 rows — hence unmistakable vertical banding in the rendered output.
--
-- Fix: regenerate the top 90 rows with PER-CELL randomization (~32% light
-- grass, ~68% dark grass, matching the existing map's ratio), preserving the
-- single-tile river at col 124. The south 90 rows (old terrain + bridge
-- tiles from ZBBS-041) are untouched.

WITH new_prefix AS (
    SELECT string_agg(
        decode(
            lpad(to_hex(
                CASE
                    WHEN (g % 200) = 124 THEN 5                 -- river
                    WHEN random() < 0.32 THEN 2                  -- light grass
                    ELSE 3                                       -- dark grass
                END
            ), 2, '0'),
            'hex'
        ),
        ''::bytea
        ORDER BY g
    ) AS pfx
    FROM generate_series(0, 90 * 200 - 1) AS g
)
UPDATE village_terrain
SET data = (SELECT pfx FROM new_prefix) || substring(data FROM (90 * 200 + 1) FOR (90 * 200)),
    updated_by = 'ZBBS-042',
    updated_at = NOW()
WHERE id = 1;
