-- LLM-678: road damage art — snapped trees and the stump they leave.
--
-- LLM-677's road obstacles were the catalog's thin fallen logs. This adds the
-- art a road break places now (engine damage_road.go roadObstacleVariants):
--
--   1. Fallen Maple / Fallen Chestnut / Fallen Birch — each the separated top
--      of a Growable Trees 1.2 tree, laid down crown-west, in two states:
--      bare (bare-autumn row) and winter (winter row). Footprints cover the
--      lying top and stop one tile short of the stump.
--   2. Storm Stump — the matching stump each snapped top leaves standing at its
--      cut end, six states (<species>-bare / <species>-winter), all drawn in
--      one common 36x32 cell with the base on the same pixel, so one anchor
--      serves all six. A 1-tile obstacle; the engine only ever places it on
--      grass beside the road, never on it.
--   3. Fallen Trunk — the summer-forest broken trunk (the "Fallen Tree"
--      scenery art) as a road variant with a 2x2 footprint. The scenery asset
--      and its placements are not touched.
--   4. village_object.expires_at — when a temporary placement is removed. A
--      stump gets one when its tree is cleared (road_stump_days, default 7);
--      the engine deletes it at the first daily boundary past that time.
--
-- Anchors and footprints are measured off the art (llm-memory-village-tiles
-- tilesets/growable-trees/public-works-fallen-trees.json): each top's anchor
-- sits on the centre of a 16-px cell of its frame, so its footprint follows the
-- tile grid.
--
-- THIS MIGRATION ALONE DOES NOT DRAW THE TREES. The sheet is gitignored and
-- travels by scp: public-works-fallen-trees.png must be at
-- /var/www/llm-memory-salem-1692/tilesets/mana-seed/growable-trees/ (owner
-- www-data). The engine still places and clears them without it.
--
-- asset / asset_state are reference data (boot-loaded); deploy.sh runs stop ->
-- migrate -> start. Asset ids are fixed (the engine names them). Category
-- "debris" is LLM-675's, so the editor files these apart from scenery.
--
-- Rerun-safe: ON CONFLICT / NOT EXISTS guarded, ADD COLUMN IF NOT EXISTS.
-- Loud validation at the end.

BEGIN;

ALTER TABLE village_object ADD COLUMN IF NOT EXISTS expires_at timestamptz;

INSERT INTO asset (
    id, name, category, default_state, anchor_x, anchor_y, layer, pack_id,
    z_index, is_obstacle, footprint_left, footprint_right, footprint_top, footprint_bottom, source_file)
VALUES
    ('019e5f00-c401-7a10-9e00-000000678001', 'Fallen Maple', 'debris', 'bare',
     0.42857142857142855, 0.5333333333333333, 'objects', 'mana-seed', 10, true, 1, 1, 1, 1,
     'composite: growable tree (maple) no shadow.png, top col 4 rows 3/4, transposed'),
    ('019e5f00-c401-7a10-9e00-000000678002', 'Fallen Chestnut', 'debris', 'bare',
     0.49382716049382713, 0.36363636363636365, 'objects', 'mana-seed', 10, true, 2, 2, 1, 2,
     'composite: growable tree (chestnut) no shadow.png, top col 4 rows 3/4, transposed'),
    ('019e5f00-c401-7a10-9e00-000000678003', 'Fallen Birch', 'debris', 'bare',
     0.5555555555555556, 0.24242424242424243, 'objects', 'mana-seed', 10, true, 2, 1, 0, 1,
     'composite: growable tree (birch) no shadow.png, top col 4 rows 3/4, transposed'),
    ('019e5f00-c401-7a10-9e00-000000678004', 'Storm Stump', 'debris', 'maple-bare',
     0.4861111111111111, 0.890625, 'objects', 'mana-seed', 10, true, 0, 0, 0, 0,
     'composite: growable tree (maple/chestnut/birch) no shadow.png, stump col 5 rows 3/4'),
    ('019e5f00-c401-7a10-9e00-000000678005', 'Fallen Trunk', 'debris', 'default',
     0.75, 0.75, 'objects', 'mana-seed', 10, true, 1, 0, 1, 0,
     'summer 32x32.png (160,0) — the Fallen Tree scenery art')
ON CONFLICT (id) DO NOTHING;

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT v.asset_id::uuid, v.state, v.sheet, v.src_x, v.src_y, v.src_w, v.src_h, 1, 0
  FROM (VALUES
    ('019e5f00-c401-7a10-9e00-000000678001', 'bare',            '/tilesets/mana-seed/growable-trees/public-works-fallen-trees.png',   0,  0, 56, 45),
    ('019e5f00-c401-7a10-9e00-000000678001', 'winter',          '/tilesets/mana-seed/growable-trees/public-works-fallen-trees.png',  81,  0, 56, 45),
    ('019e5f00-c401-7a10-9e00-000000678002', 'bare',            '/tilesets/mana-seed/growable-trees/public-works-fallen-trees.png', 162,  0, 81, 66),
    ('019e5f00-c401-7a10-9e00-000000678002', 'winter',          '/tilesets/mana-seed/growable-trees/public-works-fallen-trees.png', 243,  0, 81, 66),
    ('019e5f00-c401-7a10-9e00-000000678003', 'bare',            '/tilesets/mana-seed/growable-trees/public-works-fallen-trees.png', 324,  0, 72, 33),
    ('019e5f00-c401-7a10-9e00-000000678003', 'winter',          '/tilesets/mana-seed/growable-trees/public-works-fallen-trees.png', 405,  0, 72, 33),
    ('019e5f00-c401-7a10-9e00-000000678004', 'maple-bare',      '/tilesets/mana-seed/growable-trees/public-works-fallen-trees.png',   0, 66, 36, 32),
    ('019e5f00-c401-7a10-9e00-000000678004', 'maple-winter',    '/tilesets/mana-seed/growable-trees/public-works-fallen-trees.png',  36, 66, 36, 32),
    ('019e5f00-c401-7a10-9e00-000000678004', 'chestnut-bare',   '/tilesets/mana-seed/growable-trees/public-works-fallen-trees.png',  72, 66, 36, 32),
    ('019e5f00-c401-7a10-9e00-000000678004', 'chestnut-winter', '/tilesets/mana-seed/growable-trees/public-works-fallen-trees.png', 108, 66, 36, 32),
    ('019e5f00-c401-7a10-9e00-000000678004', 'birch-bare',      '/tilesets/mana-seed/growable-trees/public-works-fallen-trees.png', 144, 66, 36, 32),
    ('019e5f00-c401-7a10-9e00-000000678004', 'birch-winter',    '/tilesets/mana-seed/growable-trees/public-works-fallen-trees.png', 180, 66, 36, 32),
    ('019e5f00-c401-7a10-9e00-000000678005', 'default',         '/tilesets/mana-seed/summer-forest/summer sheets/summer 32x32.png', 160,  0, 32, 32)
  ) AS v(asset_id, state, sheet, src_x, src_y, src_w, src_h)
 WHERE NOT EXISTS (SELECT 1 FROM asset_state s
                    WHERE s.asset_id = v.asset_id::uuid AND s.state = v.state);

DO $$
BEGIN
    IF (SELECT count(*) FROM asset
         WHERE id IN ('019e5f00-c401-7a10-9e00-000000678001', '019e5f00-c401-7a10-9e00-000000678002',
                      '019e5f00-c401-7a10-9e00-000000678003', '019e5f00-c401-7a10-9e00-000000678004',
                      '019e5f00-c401-7a10-9e00-000000678005')
           AND is_obstacle AND category = 'debris') <> 5 THEN
        RAISE EXCEPTION 'LLM-678: the five road-obstacle assets are missing or not the rows this migration writes';
    END IF;
    IF (SELECT count(*) FROM asset_state
         WHERE asset_id IN ('019e5f00-c401-7a10-9e00-000000678001', '019e5f00-c401-7a10-9e00-000000678002',
                            '019e5f00-c401-7a10-9e00-000000678003', '019e5f00-c401-7a10-9e00-000000678004',
                            '019e5f00-c401-7a10-9e00-000000678005')) <> 13 THEN
        RAISE EXCEPTION 'LLM-678: the road-obstacle assets do not carry their 13 states';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM information_schema.columns
                    WHERE table_name = 'village_object' AND column_name = 'expires_at') THEN
        RAISE EXCEPTION 'LLM-678: village_object.expires_at is missing';
    END IF;
END $$;

COMMIT;
