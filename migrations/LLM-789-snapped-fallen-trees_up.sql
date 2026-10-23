-- LLM-789: road damage tops on the diagonal, snapped mid-trunk, in two poses.
--
-- LLM-678's tops lay flat, crown-west. On a dirt road the bare brown tops read
-- as ruts (the chestnut also as "an alien spider"). The new sheet
-- (llm-memory-village-tiles tilesets/growable-trees/
-- public-works-fallen-trees-snapped.png, built by tools/fallen-trees from the
-- LLM-678 sheet) lays each top on the diagonal, snaps it mid-trunk with a kink
-- and pale splintered ends, lifts the maple and chestnut bark, thins the
-- chestnut crown, and outlines every top.
--
-- Each species comes in two poses. Anchor and footprint are per asset, so each
-- pose is its own asset; bare and winter share one frame size within a pose.
--
--   1. Fallen Maple / Chestnut / Birch (LLM-678 ids): now crown up-left, stump
--      down-right. New anchor, footprint and source_file; both states move to
--      the new sheet.
--   2. Fallen Maple / Chestnut / Birch (SW) (new ids ...789001-003): crown
--      down-left, stump up-right, states bare and winter.
--
-- The Storm Stump and the Fallen Trunk are not touched (the stumps stay on the
-- LLM-678 sheet). Stump offsets live in the engine (damage_road.go
-- roadObstacleVariants) and come from the same JSON as the numbers below.
--
-- A top already placed when this runs keeps its tile and is drawn in the new
-- pose; its stump stays where it was placed. Clearing it is unchanged.
--
-- THIS MIGRATION ALONE DOES NOT DRAW THE NEW TREES. The sheet is gitignored and
-- travels by scp: public-works-fallen-trees-snapped.png must be at
-- /var/www/llm-memory-salem-1692/tilesets/mana-seed/growable-trees/ (owner
-- www-data) BEFORE this runs.
--
-- asset / asset_state are reference data (boot-loaded); deploy.sh runs stop ->
-- migrate -> start.
--
-- Rerun-safe: UPDATEs set fixed values; INSERTs are ON CONFLICT / NOT EXISTS
-- guarded. Loud validation at the end.

BEGIN;

UPDATE asset AS a
   SET name = v.name, anchor_x = v.anchor_x, anchor_y = v.anchor_y,
       footprint_left = v.fl, footprint_right = v.fr, footprint_top = v.ft, footprint_bottom = v.fb,
       source_file = v.source_file
  FROM (VALUES
    ('019e5f00-c401-7a10-9e00-000000678001', 'Fallen Maple (NW)',
     0.5797101449275363, 0.5555555555555556, 1, 1, 1, 1,
     'public-works-fallen-trees-snapped.png (tools/fallen-trees), maple pose up'),
    ('019e5f00-c401-7a10-9e00-000000678002', 'Fallen Chestnut (NW)',
     0.5194805194805194, 0.5063291139240507, 2, 2, 2, 2,
     'public-works-fallen-trees-snapped.png (tools/fallen-trees), chestnut pose up'),
    ('019e5f00-c401-7a10-9e00-000000678003', 'Fallen Birch (NW)',
     0.5063291139240507, 0.4528301886792453, 1, 2, 1, 1,
     'public-works-fallen-trees-snapped.png (tools/fallen-trees), birch pose up')
  ) AS v(id, name, anchor_x, anchor_y, fl, fr, ft, fb, source_file)
 WHERE a.id = v.id::uuid;

INSERT INTO asset (
    id, name, category, default_state, anchor_x, anchor_y, layer, pack_id,
    z_index, is_obstacle, footprint_left, footprint_right, footprint_top, footprint_bottom, source_file)
VALUES
    ('019e5f00-c401-7a10-9e00-000000789001', 'Fallen Maple (SW)', 'debris', 'bare',
     0.5633802816901409, 0.4444444444444444, 'objects', 'mana-seed', 10, true, 2, 1, 1, 1,
     'public-works-fallen-trees-snapped.png (tools/fallen-trees), maple pose down'),
    ('019e5f00-c401-7a10-9e00-000000789002', 'Fallen Chestnut (SW)', 'debris', 'bare',
     0.5544554455445545, 0.43636363636363634, 'objects', 'mana-seed', 10, true, 2, 2, 1, 1,
     'public-works-fallen-trees-snapped.png (tools/fallen-trees), chestnut pose down'),
    ('019e5f00-c401-7a10-9e00-000000789003', 'Fallen Birch (SW)', 'debris', 'bare',
     0.4878048780487805, 0.5217391304347826, 'objects', 'mana-seed', 10, true, 2, 2, 1, 1,
     'public-works-fallen-trees-snapped.png (tools/fallen-trees), birch pose down')
ON CONFLICT (id) DO NOTHING;

UPDATE asset_state AS s
   SET sheet = '/tilesets/mana-seed/growable-trees/public-works-fallen-trees-snapped.png',
       src_x = v.src_x, src_y = v.src_y, src_w = v.src_w, src_h = v.src_h
  FROM (VALUES
    ('019e5f00-c401-7a10-9e00-000000678001', 'bare',     0,   0,  69, 72),
    ('019e5f00-c401-7a10-9e00-000000678001', 'winter',  71,   0,  69, 72),
    ('019e5f00-c401-7a10-9e00-000000678002', 'bare',     0,  74,  77, 79),
    ('019e5f00-c401-7a10-9e00-000000678002', 'winter',  79,  74,  77, 79),
    ('019e5f00-c401-7a10-9e00-000000678003', 'bare',     0, 155,  79, 53),
    ('019e5f00-c401-7a10-9e00-000000678003', 'winter',  81, 155,  79, 53)
  ) AS v(asset_id, state, src_x, src_y, src_w, src_h)
 WHERE s.asset_id = v.asset_id::uuid AND s.state = v.state;

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT v.asset_id::uuid, v.state, '/tilesets/mana-seed/growable-trees/public-works-fallen-trees-snapped.png',
       v.src_x, v.src_y, v.src_w, v.src_h, 1, 0
  FROM (VALUES
    ('019e5f00-c401-7a10-9e00-000000789001', 'bare',   142,   0,  71, 54),
    ('019e5f00-c401-7a10-9e00-000000789001', 'winter', 215,   0,  71, 54),
    ('019e5f00-c401-7a10-9e00-000000789002', 'bare',   158,  74, 101, 55),
    ('019e5f00-c401-7a10-9e00-000000789002', 'winter', 261,  74, 101, 55),
    ('019e5f00-c401-7a10-9e00-000000789003', 'bare',   162, 155,  82, 46),
    ('019e5f00-c401-7a10-9e00-000000789003', 'winter', 246, 155,  82, 46)
  ) AS v(asset_id, state, src_x, src_y, src_w, src_h)
 WHERE NOT EXISTS (SELECT 1 FROM asset_state s
                    WHERE s.asset_id = v.asset_id::uuid AND s.state = v.state);

DO $$
BEGIN
    IF (SELECT count(*) FROM asset
         WHERE id IN ('019e5f00-c401-7a10-9e00-000000678001', '019e5f00-c401-7a10-9e00-000000678002',
                      '019e5f00-c401-7a10-9e00-000000678003', '019e5f00-c401-7a10-9e00-000000789001',
                      '019e5f00-c401-7a10-9e00-000000789002', '019e5f00-c401-7a10-9e00-000000789003')
           AND is_obstacle AND category = 'debris'
           AND source_file LIKE 'public-works-fallen-trees-snapped.png%') <> 6 THEN
        RAISE EXCEPTION 'LLM-789: the six snapped-top assets are missing or not the rows this migration writes';
    END IF;
    IF (SELECT count(*) FROM asset_state
         WHERE asset_id IN ('019e5f00-c401-7a10-9e00-000000678001', '019e5f00-c401-7a10-9e00-000000678002',
                            '019e5f00-c401-7a10-9e00-000000678003', '019e5f00-c401-7a10-9e00-000000789001',
                            '019e5f00-c401-7a10-9e00-000000789002', '019e5f00-c401-7a10-9e00-000000789003')
           AND state IN ('bare', 'winter')
           AND sheet = '/tilesets/mana-seed/growable-trees/public-works-fallen-trees-snapped.png') <> 12 THEN
        RAISE EXCEPTION 'LLM-789: the snapped tops do not carry their 12 bare/winter states on the new sheet';
    END IF;
END $$;

COMMIT;
