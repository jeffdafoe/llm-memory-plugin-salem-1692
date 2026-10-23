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
--   1. Fallen Maple / Chestnut / Birch (NW) — the LLM-678 ids: crown up-left,
--      stump down-right.
--   2. Fallen Maple / Chestnut / Birch (SW) — new ids ...789001-003: crown
--      down-left, stump up-right.
--
-- Every number below is the tool's JSON (copied into the engine as
-- engine/sim/testdata/fallen-trees-snapped.json; an integration test checks
-- these rows against it, and a unit test checks the engine's stump offsets).
--
-- The Storm Stump and the Fallen Trunk are not touched (the stumps stay on the
-- LLM-678 sheet).
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
-- Rerun-safe and converging: every row is upserted to the values below, and
-- the validation compares them exactly.

BEGIN;

CREATE TEMP TABLE llm789_top (
    id uuid PRIMARY KEY, name text, source_file text,
    anchor_x double precision, anchor_y double precision,
    fl int, fr int, ft int, fb int
) ON COMMIT DROP;

INSERT INTO llm789_top VALUES
    ('019e5f00-c401-7a10-9e00-000000678001', 'Fallen Maple (NW)',
     'public-works-fallen-trees-snapped.png (tools/fallen-trees), maple pose up',
     0.5797101449275363, 0.5555555555555556, 1, 1, 1, 1),
    ('019e5f00-c401-7a10-9e00-000000678002', 'Fallen Chestnut (NW)',
     'public-works-fallen-trees-snapped.png (tools/fallen-trees), chestnut pose up',
     0.5194805194805194, 0.5063291139240507, 2, 2, 2, 2),
    ('019e5f00-c401-7a10-9e00-000000678003', 'Fallen Birch (NW)',
     'public-works-fallen-trees-snapped.png (tools/fallen-trees), birch pose up',
     0.5063291139240507, 0.4528301886792453, 1, 2, 1, 1),
    ('019e5f00-c401-7a10-9e00-000000789001', 'Fallen Maple (SW)',
     'public-works-fallen-trees-snapped.png (tools/fallen-trees), maple pose down',
     0.5714285714285714, 0.42857142857142855, 1, 1, 1, 1),
    ('019e5f00-c401-7a10-9e00-000000789002', 'Fallen Chestnut (SW)',
     'public-works-fallen-trees-snapped.png (tools/fallen-trees), chestnut pose down',
     0.49382716049382713, 0.4878048780487805, 2, 2, 1, 2),
    ('019e5f00-c401-7a10-9e00-000000789003', 'Fallen Birch (SW)',
     'public-works-fallen-trees-snapped.png (tools/fallen-trees), birch pose down',
     0.5063291139240507, 0.4528301886792453, 1, 2, 1, 1);

CREATE TEMP TABLE llm789_state (
    asset_id uuid, state text, src_x int, src_y int, src_w int, src_h int,
    PRIMARY KEY (asset_id, state)
) ON COMMIT DROP;

INSERT INTO llm789_state VALUES
    ('019e5f00-c401-7a10-9e00-000000678001', 'bare',     0,   0, 69, 72),
    ('019e5f00-c401-7a10-9e00-000000678001', 'winter',  71,   0, 69, 72),
    ('019e5f00-c401-7a10-9e00-000000678002', 'bare',     0,  74, 77, 79),
    ('019e5f00-c401-7a10-9e00-000000678002', 'winter',  79,  74, 77, 79),
    ('019e5f00-c401-7a10-9e00-000000678003', 'bare',     0, 158, 79, 53),
    ('019e5f00-c401-7a10-9e00-000000678003', 'winter',  81, 158, 79, 53),
    ('019e5f00-c401-7a10-9e00-000000789001', 'bare',   142,   0, 70, 56),
    ('019e5f00-c401-7a10-9e00-000000789001', 'winter', 214,   0, 70, 56),
    ('019e5f00-c401-7a10-9e00-000000789002', 'bare',   158,  74, 81, 82),
    ('019e5f00-c401-7a10-9e00-000000789002', 'winter', 241,  74, 81, 82),
    ('019e5f00-c401-7a10-9e00-000000789003', 'bare',   162, 158, 79, 53),
    ('019e5f00-c401-7a10-9e00-000000789003', 'winter', 243, 158, 79, 53);

INSERT INTO asset (
    id, name, category, default_state, anchor_x, anchor_y, layer, pack_id,
    z_index, is_obstacle, footprint_left, footprint_right, footprint_top, footprint_bottom, source_file)
SELECT id, name, 'debris', 'bare', anchor_x, anchor_y, 'objects', 'mana-seed',
       10, true, fl, fr, ft, fb, source_file
  FROM llm789_top
ON CONFLICT (id) DO UPDATE SET
    name = EXCLUDED.name, category = EXCLUDED.category, default_state = EXCLUDED.default_state,
    anchor_x = EXCLUDED.anchor_x, anchor_y = EXCLUDED.anchor_y,
    layer = EXCLUDED.layer, pack_id = EXCLUDED.pack_id, z_index = EXCLUDED.z_index,
    is_obstacle = EXCLUDED.is_obstacle,
    footprint_left = EXCLUDED.footprint_left, footprint_right = EXCLUDED.footprint_right,
    footprint_top = EXCLUDED.footprint_top, footprint_bottom = EXCLUDED.footprint_bottom,
    source_file = EXCLUDED.source_file;

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT asset_id, state, '/tilesets/mana-seed/growable-trees/public-works-fallen-trees-snapped.png',
       src_x, src_y, src_w, src_h, 1, 0
  FROM llm789_state
ON CONFLICT (asset_id, state) DO UPDATE SET
    sheet = EXCLUDED.sheet,
    src_x = EXCLUDED.src_x, src_y = EXCLUDED.src_y, src_w = EXCLUDED.src_w, src_h = EXCLUDED.src_h,
    frame_count = EXCLUDED.frame_count, frame_rate = EXCLUDED.frame_rate;

DO $$
BEGIN
    IF (SELECT count(*) FROM asset a JOIN llm789_top v ON v.id = a.id
         WHERE a.name = v.name AND a.source_file = v.source_file AND a.category = 'debris'
           AND a.default_state = 'bare' AND a.is_obstacle
           AND a.layer = 'objects' AND a.pack_id = 'mana-seed' AND a.z_index = 10
           AND a.anchor_x = v.anchor_x AND a.anchor_y = v.anchor_y
           AND a.footprint_left = v.fl AND a.footprint_right = v.fr
           AND a.footprint_top = v.ft AND a.footprint_bottom = v.fb) <> 6 THEN
        RAISE EXCEPTION 'LLM-789: the six snapped-top assets do not carry their anchors and footprints';
    END IF;
    IF (SELECT count(*) FROM asset_state s JOIN llm789_state v ON v.asset_id = s.asset_id AND v.state = s.state
         WHERE s.sheet = '/tilesets/mana-seed/growable-trees/public-works-fallen-trees-snapped.png'
           AND s.src_x = v.src_x AND s.src_y = v.src_y AND s.src_w = v.src_w AND s.src_h = v.src_h
           AND s.frame_count = 1 AND s.frame_rate = 0) <> 12 THEN
        RAISE EXCEPTION 'LLM-789: the snapped tops do not carry their 12 frames on the new sheet';
    END IF;
    IF EXISTS (SELECT 1 FROM asset_state s JOIN llm789_top t ON t.id = s.asset_id
                WHERE s.state NOT IN ('bare', 'winter')) THEN
        RAISE EXCEPTION 'LLM-789: a snapped-top asset carries a state other than bare and winter';
    END IF;
END $$;

COMMIT;
