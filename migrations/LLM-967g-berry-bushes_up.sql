-- LLM-967: painted berry and sage bushes, three of each, with winter looks.
--
-- The Blueberry Bush, Raspberry Bush and Sage Bush drew Mana Seed art, whose
-- licence forbids use alongside AI. tools/tree-art now paints them as it
-- paints the trees: client/assets/trees/blueberry-1.png, -2, -3, raspberry-1..3
-- and sage-1..3, three bushes of each grown from different seeds. This
-- migration sorts after LLM-967f-fallen-trees (salem migrations run in
-- filename order).
--
-- Sheet layout (build.py): one row per look, 36 frames left to right played at
-- 6 fps (every look is leafy, so every look rustles). Frames are 16x32 for the
-- blueberry, 32x32 for the raspberry, 16x16 for the sage. Rows, top to bottom:
--   blueberry, raspberry: berries, bare, autumn-berries, autumn-bare,
--     winter-berries, winter-bare, winter-snowy-berries, winter-snowy-bare
--   sage: default, autumn, winter, winter-snowy
-- A picked look is its fruit look without the fruit.
--
-- Winter (Jeff 2026-11-20): berries keep growing in winter at half speed
-- (winter_regrow_factors blueberries=200, raspberries=200, unchanged here), so
-- a berry bush needs winter art with berries as well as without. The winter
-- states are tagged with the stock tag (berries / bare) and the season look
-- (season-winter, season-winter-snowy); the season chain
-- (engine/sim/season_art.go) shows `winter` from December to March and
-- `winter-snowy` when snow lies. Sage is evergreen: winter and winter-snowy,
-- season tag only. Until now these bushes had no winter states and showed
-- their autumn art all winter.
--
-- WHAT this does, for each of the three bushes that exists (a fresh database
-- has no catalog; then it does nothing):
--   1. Its states point at <bush>-1.png in that layout, and it gets the winter
--      states, tagged exactly as above. The tags of the summer and autumn
--      states are untouched.
--   2. <Bush> 2 and <Bush> 3 (pinned ids) are its copies: every asset column
--      but id and name, every state on <bush>-2/-3, the same tags, and its rest
--      default (asset_refresh_default), so a new placement fruits alike.
--   3. The placed bushes (pinned ids: 40 blueberry, 24 raspberry, 3 sage) are
--      spread over the three, chosen so that no bush's nearest or second
--      nearest bush of its kind is the same variant. Their current state and
--      object_refresh rows (berries left) stay with them.
-- Then every one of those must hold exactly, or the migration fails.
--
-- ENGINE-OWNED data (catalog and village_object), read at boot; deploy.sh
-- runs stop -> migrate -> start. Convergent: rerunning ends in the same place,
-- and a rerun repairs a hand edit of these rows.

BEGIN;

DO $$
DECLARE
    blueberry CONSTANT uuid := '630909ca-df4f-43ac-9fc4-5192ca44da73';
    raspberry CONSTANT uuid := 'db4b428c-9ab6-4457-85fb-3f85fe86c946';
    sage CONSTANT uuid := 'bad3e45f-3649-4cf2-b9b2-cda95b334113';
    bad integer;
BEGIN
    -- The nine assets: each bush and its two copies, their sheets and frame.
    CREATE TEMP TABLE llm967g_asset (asset_id uuid PRIMARY KEY, base_id uuid NOT NULL, bush text NOT NULL,
                                     variant integer NOT NULL, name text NOT NULL, sheet text NOT NULL,
                                     frame_w integer NOT NULL, frame_h integer NOT NULL) ON COMMIT DROP;
    INSERT INTO llm967g_asset VALUES
        (blueberry,                              blueberry, 'blueberry', 1, 'Blueberry Bush',   'res://assets/trees/blueberry-1.png', 16, 32),
        ('06a3858b-d75e-44e2-a896-a29cdf0a09a7', blueberry, 'blueberry', 2, 'Blueberry Bush 2', 'res://assets/trees/blueberry-2.png', 16, 32),
        ('1c378426-eb00-4fb9-a474-b6e61e57f925', blueberry, 'blueberry', 3, 'Blueberry Bush 3', 'res://assets/trees/blueberry-3.png', 16, 32),
        (raspberry,                              raspberry, 'raspberry', 1, 'Raspberry Bush',   'res://assets/trees/raspberry-1.png', 32, 32),
        ('67e5be5e-d955-4d6b-b119-ad40f0c51f5e', raspberry, 'raspberry', 2, 'Raspberry Bush 2', 'res://assets/trees/raspberry-2.png', 32, 32),
        ('e6ac2605-cd55-484b-ba4a-857686219df9', raspberry, 'raspberry', 3, 'Raspberry Bush 3', 'res://assets/trees/raspberry-3.png', 32, 32),
        (sage,                                   sage,      'sage',      1, 'Sage Bush',        'res://assets/trees/sage-1.png',      16, 16),
        ('55e56e90-55f0-4d53-88d5-5e2b9a342dca', sage,      'sage',      2, 'Sage Bush 2',      'res://assets/trees/sage-2.png',      16, 16),
        ('a48e0653-24ee-4009-a303-b19558c0b233', sage,      'sage',      3, 'Sage Bush 3',      'res://assets/trees/sage-3.png',      16, 16);
    -- A bush that is not in this database takes no part.
    DELETE FROM llm967g_asset v WHERE NOT EXISTS (SELECT 1 FROM asset WHERE id = v.base_id);
    IF NOT EXISTS (SELECT 1 FROM llm967g_asset) THEN
        RETURN;
    END IF;

    -- Each look's row on its bush's sheets. tags is set for the winter states
    -- this migration adds (written exactly); NULL for the summer and autumn
    -- states, whose tags it leaves alone.
    CREATE TEMP TABLE llm967g_look (bush text NOT NULL, state text NOT NULL, row_index integer NOT NULL, tags text[],
                                    PRIMARY KEY (bush, state)) ON COMMIT DROP;
    INSERT INTO llm967g_look VALUES
        ('blueberry', 'berries',              0, NULL),
        ('blueberry', 'bare',                 1, NULL),
        ('blueberry', 'autumn-berries',       2, NULL),
        ('blueberry', 'autumn-bare',          3, NULL),
        ('blueberry', 'winter-berries',       4, ARRAY['berries', 'season-winter']),
        ('blueberry', 'winter-bare',          5, ARRAY['bare', 'season-winter']),
        ('blueberry', 'winter-snowy-berries', 6, ARRAY['berries', 'season-winter-snowy']),
        ('blueberry', 'winter-snowy-bare',    7, ARRAY['bare', 'season-winter-snowy']),
        ('raspberry', 'berries',              0, NULL),
        ('raspberry', 'bare',                 1, NULL),
        ('raspberry', 'autumn-berries',       2, NULL),
        ('raspberry', 'autumn-bare',          3, NULL),
        ('raspberry', 'winter-berries',       4, ARRAY['berries', 'season-winter']),
        ('raspberry', 'winter-bare',          5, ARRAY['bare', 'season-winter']),
        ('raspberry', 'winter-snowy-berries', 6, ARRAY['berries', 'season-winter-snowy']),
        ('raspberry', 'winter-snowy-bare',    7, ARRAY['bare', 'season-winter-snowy']),
        ('sage',      'default',              0, NULL),
        ('sage',      'autumn',               1, NULL),
        ('sage',      'winter',               2, ARRAY['season-winter']),
        ('sage',      'winter-snowy',         3, ARRAY['season-winter-snowy']);

    -- Every state of every asset, as authored: 36 frames at 6 fps in its row.
    CREATE TEMP TABLE llm967g_state ON COMMIT DROP AS
    SELECT v.asset_id, l.state, v.sheet, 0 AS src_x, l.row_index * v.frame_h AS src_y, v.frame_w AS src_w,
           v.frame_h AS src_h, 36 AS frame_count, 6::double precision AS frame_rate
      FROM llm967g_asset v JOIN llm967g_look l ON l.bush = v.bush;

    -- 2. The new assets: their bush's columns, their own id and name.
    INSERT INTO asset (id, name, category, default_state, anchor_x, anchor_y, layer, pack_id, interior, fits_slot,
                       source_file, rotation_algo, transition_spread_seconds, is_obstacle, is_passage, z_index,
                       footprint_left, footprint_right, footprint_top, footprint_bottom, door_offset_x, door_offset_y,
                       visible_when_inside, stand_offset_x, stand_offset_y, occupied_min_count, occupied_night_only, render_scale)
    SELECT v.asset_id, v.name, m.category, m.default_state, m.anchor_x, m.anchor_y, m.layer, m.pack_id, m.interior, m.fits_slot,
           m.source_file, m.rotation_algo, m.transition_spread_seconds, m.is_obstacle, m.is_passage, m.z_index,
           m.footprint_left, m.footprint_right, m.footprint_top, m.footprint_bottom, m.door_offset_x, m.door_offset_y,
           m.visible_when_inside, m.stand_offset_x, m.stand_offset_y, m.occupied_min_count, m.occupied_night_only, m.render_scale
      FROM llm967g_asset v JOIN asset m ON m.id = v.base_id
     WHERE v.asset_id <> v.base_id
    ON CONFLICT (id) DO UPDATE
       SET name = EXCLUDED.name, category = EXCLUDED.category, default_state = EXCLUDED.default_state,
           anchor_x = EXCLUDED.anchor_x, anchor_y = EXCLUDED.anchor_y, layer = EXCLUDED.layer,
           pack_id = EXCLUDED.pack_id, interior = EXCLUDED.interior, fits_slot = EXCLUDED.fits_slot,
           source_file = EXCLUDED.source_file, rotation_algo = EXCLUDED.rotation_algo,
           transition_spread_seconds = EXCLUDED.transition_spread_seconds, is_obstacle = EXCLUDED.is_obstacle,
           is_passage = EXCLUDED.is_passage, z_index = EXCLUDED.z_index,
           footprint_left = EXCLUDED.footprint_left, footprint_right = EXCLUDED.footprint_right,
           footprint_top = EXCLUDED.footprint_top, footprint_bottom = EXCLUDED.footprint_bottom,
           door_offset_x = EXCLUDED.door_offset_x, door_offset_y = EXCLUDED.door_offset_y,
           visible_when_inside = EXCLUDED.visible_when_inside, stand_offset_x = EXCLUDED.stand_offset_x,
           stand_offset_y = EXCLUDED.stand_offset_y, occupied_min_count = EXCLUDED.occupied_min_count,
           occupied_night_only = EXCLUDED.occupied_night_only, render_scale = EXCLUDED.render_scale;

    -- 1 and 2. Every look of every bush on its sheet, in its row.
    INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
    SELECT asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate
      FROM llm967g_state
    ON CONFLICT (asset_id, state) DO UPDATE
       SET sheet = EXCLUDED.sheet, src_x = EXCLUDED.src_x, src_y = EXCLUDED.src_y, src_w = EXCLUDED.src_w,
           src_h = EXCLUDED.src_h, frame_count = EXCLUDED.frame_count, frame_rate = EXCLUDED.frame_rate;

    -- 1. The winter states' tags on each bush, exactly.
    DELETE FROM asset_state_tag
     WHERE state_id IN (SELECT s.id FROM asset_state s
                          JOIN llm967g_asset v ON v.asset_id = s.asset_id AND v.asset_id = v.base_id
                          JOIN llm967g_look l ON l.bush = v.bush AND l.state = s.state
                         WHERE l.tags IS NOT NULL);
    INSERT INTO asset_state_tag (state_id, tag)
    SELECT s.id, t.tag
      FROM asset_state s
      JOIN llm967g_asset v ON v.asset_id = s.asset_id AND v.asset_id = v.base_id
      JOIN llm967g_look l ON l.bush = v.bush AND l.state = s.state
      CROSS JOIN LATERAL unnest(l.tags) t(tag)
     WHERE l.tags IS NOT NULL;

    -- 2. The copies carry exactly their bush's tags, state by state: cleared
    -- and copied fresh, so a rerun repairs a hand edit.
    DELETE FROM asset_state_tag
     WHERE state_id IN (SELECT s.id FROM asset_state s
                          JOIN llm967g_asset v ON v.asset_id = s.asset_id AND v.asset_id <> v.base_id);
    INSERT INTO asset_state_tag (state_id, tag)
    SELECT vs.id, t.tag
      FROM llm967g_asset v
      JOIN asset_state ms ON ms.asset_id = v.base_id
      JOIN asset_state_tag t ON t.state_id = ms.id
      JOIN asset_state vs ON vs.asset_id = v.asset_id AND vs.state = ms.state
     WHERE v.asset_id <> v.base_id;

    -- Its rest default, for new placements of the new assets: the copies'
    -- rows are replaced by a fresh copy, so a rerun repairs a hand edit.
    DELETE FROM asset_refresh_default
     WHERE asset_id IN (SELECT asset_id FROM llm967g_asset WHERE asset_id <> base_id);
    INSERT INTO asset_refresh_default (asset_id, attribute, amount, available_quantity, max_quantity, refresh_mode,
                                       refresh_period_hours, dwell_amount, dwell_period_minutes, gather_item)
    SELECT v.asset_id, d.attribute, d.amount, d.available_quantity, d.max_quantity, d.refresh_mode,
           d.refresh_period_hours, d.dwell_amount, d.dwell_period_minutes, d.gather_item
      FROM llm967g_asset v JOIN asset_refresh_default d ON d.asset_id = v.base_id
     WHERE v.asset_id <> v.base_id;

    -- 3. The placed bushes, each to its variant, west to east.
    CREATE TEMP TABLE llm967g_place (object_id uuid PRIMARY KEY, bush text NOT NULL, variant integer NOT NULL) ON COMMIT DROP;
    INSERT INTO llm967g_place VALUES
        ('019da69f-4d2a-7f13-8c98-013e1681519c', 'blueberry', 1),  -- -1257, -1097
        ('019da69e-c96f-71a7-9ca9-013b5e0f59f7', 'blueberry', 2),  -- -1256, -1230
        ('019da6a0-613b-7041-bf08-8c9b9c9fe707', 'blueberry', 3),  -- -1254, -838
        ('019da6a0-0cf1-7ddb-927b-b323e1f552ea', 'blueberry', 1),  -- -1251, -910
        ('019da6a0-ab60-7206-a82f-5080c259f94d', 'blueberry', 2),  -- -1250, -767
        ('019da69e-ad72-79ed-ac7b-5f8ddd5f2e40', 'blueberry', 3),  -- -1248, -1290
        ('019da69f-122e-7709-b252-e9e1740e8faf', 'blueberry', 3),  -- -1248, -1160
        ('019da69f-d1f9-79c1-a5e0-231b95330709', 'blueberry', 3),  -- -1247, -962
        ('019da69f-b003-7d07-9d1d-fd2d9dfc301d', 'blueberry', 3),  -- -1245, -1049
        ('019da69f-6513-77df-bb7f-8c5dc7ebb584', 'blueberry', 3),  -- -1197, -1105
        ('019da69f-9308-71cb-95d4-c5612fee36ff', 'blueberry', 1),  -- -1194, -1041
        ('019da69e-8faa-78e3-b692-773a8ab3cd86', 'blueberry', 2),  -- -1188, -1285
        ('019da6a0-2acf-7986-a4ea-0a448232a484', 'blueberry', 2),  -- -1188, -913
        ('019da6a0-8279-70b6-9578-981bea479f9f', 'blueberry', 1),  -- -1188, -853
        ('019da6a0-c7d5-7ec7-8220-3d3710d501db', 'blueberry', 3),  -- -1188, -785
        ('019da69f-f0ad-736a-9c39-2a953448ec74', 'blueberry', 1),  -- -1186, -968
        ('019da69d-c72d-7455-836f-08758f7968bc', 'blueberry', 1),  -- -1183, -1216
        ('019da69f-366a-799f-8e1a-8485a7f97ef9', 'blueberry', 2),  -- -1179, -1157
        ('019da6a4-3bf4-742e-90a0-411d00bc00ff', 'blueberry', 2),  -- -1146, -829
        ('019da6a3-8a95-7f55-bd18-e359aeeca693', 'blueberry', 2),  -- -1123, -1032
        ('019da6a3-33ae-7132-917d-068487774665', 'blueberry', 1),  -- -1108, -1162
        ('019da6a3-e874-7fa8-a211-168c03ed4f9a', 'blueberry', 3),  -- -1108, -959
        ('019da6a3-1e95-7f0f-802e-03a7cc44d36d', 'blueberry', 2),  -- -1105, -1215
        ('019da6a4-706a-779d-adc1-a072df50833a', 'blueberry', 1),  -- -1104, -790
        ('019da6a2-eb05-7a11-975d-8f3fc329ad79', 'blueberry', 1),  -- -1102, -1272
        ('019da6a3-063f-74e0-98a5-34c1925da2eb', 'blueberry', 3),  -- -1101, -1230
        ('019da6a3-4b08-7e38-bc8b-e4312f6a4c02', 'blueberry', 2),  -- -1099, -1118
        ('019da6a4-214a-7123-b093-56d605d708df', 'blueberry', 3),  -- -1098, -885
        ('019da6a3-6a04-734a-a231-cac5a5c8911d', 'blueberry', 3),  -- -1096, -1085
        ('019da6a3-b32e-713c-aff6-0a5ca4c04f2c', 'blueberry', 1),  -- -1075, -1025
        ('019da6a4-520c-72bf-a45d-6328d3faf4d8', 'blueberry', 2),  -- -1073, -831
        ('019da6a4-02fd-7d75-8a9e-3382a9b14031', 'blueberry', 2),  -- -1067, -940
        ('019d98b8-e68f-7919-93e0-3a592797c73f', 'blueberry', 1),  -- 762, 1130
        ('019d98b8-7a4b-757e-a70a-96076633d0db', 'blueberry', 1),  -- 767, 1083
        ('019d98b8-bb9d-7185-b8a3-3c32435b6569', 'blueberry', 2),  -- 769, 1112
        ('019d98b8-f75a-7441-af9c-e8b8addae500', 'blueberry', 3),  -- 781, 1139
        ('019d98b8-d5f7-7c16-8c95-8648f0c4c54d', 'blueberry', 1),  -- 791, 1120
        ('019d98b8-9c42-787e-8cbf-c91f99f8cbef', 'blueberry', 3),  -- 794, 1094
        ('019d98b9-1bb9-77e9-ae7b-6b061aa64e1f', 'blueberry', 2),  -- 808, 1119
        ('019d98b9-0ad3-725a-9aaa-cb11ef87a152', 'blueberry', 1),  -- 818, 1094
        ('019da6a1-675b-783d-ae03-29ece5fe5ced', 'raspberry', 3),  -- -1399, -1183
        ('019da6a1-3007-7822-a266-1257bc65f3a6', 'raspberry', 1),  -- -1394, -1255
        ('019da6a1-e84f-7c1c-ac28-c47b1fd5bb04', 'raspberry', 3),  -- -1391, -968
        ('019da6a2-c17f-7c0b-9485-32fe984a21db', 'raspberry', 3),  -- -1391, -761
        ('019da6a1-cce9-7233-8dff-bd93ee0b782a', 'raspberry', 1),  -- -1388, -1053
        ('019da6a2-6f0f-7ac5-8b7a-490b1289122b', 'raspberry', 2),  -- -1388, -832
        ('019da6a1-9763-7758-afa1-2192e74b60a4', 'raspberry', 2),  -- -1382, -1114
        ('019da6a2-478f-796f-b1be-1b8d2a29eb72', 'raspberry', 1),  -- -1378, -916
        ('019da6a2-a94a-7721-9c67-3cc3e23711c0', 'raspberry', 2),  -- -1324, -763
        ('019da6a1-487e-7211-abc3-5b07d267841f', 'raspberry', 3),  -- -1316, -1183
        ('019da6a2-20fb-7641-9c38-e4066f599bbe', 'raspberry', 3),  -- -1314, -906
        ('019da6a1-19f9-7ae8-90cf-6856a6c6cdb6', 'raspberry', 2),  -- -1313, -1260
        ('019da6a2-8bfc-7951-adf7-a4c05c3813cb', 'raspberry', 1),  -- -1311, -840
        ('019da6a1-7de4-7427-a3f9-823f514389bb', 'raspberry', 1),  -- -1310, -1111
        ('019da6a1-b357-7ed0-a28f-6be638687950', 'raspberry', 2),  -- -1310, -1038
        ('019da6a2-0150-76af-b4fd-fabde9fb1efb', 'raspberry', 1),  -- -1310, -981
        ('019d79ef-d9de-7fca-8541-a947d1dbe40b', 'raspberry', 1),  -- 369, 143
        ('019dc5f2-6281-7a0e-82c3-e66f10d04847', 'raspberry', 2),  -- 437, -1417
        ('019dc5f2-85c9-7851-86a3-6ac4e248e660', 'raspberry', 3),  -- 614, -1416
        ('019d79ef-d9df-7efc-bc7f-36f2ce4f2deb', 'raspberry', 2),  -- 615, 481
        ('019d98b3-84e4-73bb-8f13-d33c05638f0f', 'raspberry', 3),  -- 2209, 1171
        ('019d98b3-a85d-72eb-9faf-ad3e6642c71b', 'raspberry', 2),  -- 2269, 1171
        ('019d98b5-1e22-7aae-87be-d1afb0cb7241', 'raspberry', 1),  -- 2593, 1172
        ('019d98b5-0313-731f-a0f2-93edb758b3dd', 'raspberry', 3),  -- 2651, 1161
        ('019dcad0-036d-7ac1-9798-d7243a3a2886', 'sage', 1),       -- -50, -1336
        ('995f75fd-50c5-4bc9-a868-300adca460cb', 'sage', 3),       -- 266, -3278
        ('c67c9f28-bd48-4652-ab21-2a5c18ec2cb3', 'sage', 2);       -- 321, -3286
    -- Only a bush still on one of its own kind's assets moves (a placed id
    -- hand-switched to some other asset is left alone).
    UPDATE village_object o
       SET asset_id = v.asset_id
      FROM llm967g_place p
      JOIN llm967g_asset v ON v.bush = p.bush AND v.variant = p.variant
     WHERE o.id = p.object_id
       AND o.asset_id IN (SELECT asset_id FROM llm967g_asset WHERE bush = p.bush);

    -- Every bush asset has exactly its states, with exactly these values.
    SELECT count(*) INTO bad FROM (
        (SELECT asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate FROM llm967g_state
         EXCEPT
         SELECT s.asset_id, s.state, s.sheet, s.src_x, s.src_y, s.src_w, s.src_h, s.frame_count, s.frame_rate
           FROM asset_state s JOIN llm967g_asset v ON v.asset_id = s.asset_id)
        UNION ALL
        (SELECT s.asset_id, s.state, s.sheet, s.src_x, s.src_y, s.src_w, s.src_h, s.frame_count, s.frame_rate
           FROM asset_state s JOIN llm967g_asset v ON v.asset_id = s.asset_id
         EXCEPT
         SELECT asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate FROM llm967g_state)
    ) diff;
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-967g: % bush state rows differ from the authored values', bad;
    END IF;

    -- Each bush's winter states carry exactly their authored tags.
    SELECT count(*) INTO bad FROM (
        (SELECT s.id, t.tag
           FROM asset_state s
           JOIN llm967g_asset v ON v.asset_id = s.asset_id AND v.asset_id = v.base_id
           JOIN llm967g_look l ON l.bush = v.bush AND l.state = s.state
           CROSS JOIN LATERAL unnest(l.tags) t(tag)
          WHERE l.tags IS NOT NULL
         EXCEPT
         SELECT s.id, t.tag
           FROM asset_state s
           JOIN llm967g_asset v ON v.asset_id = s.asset_id AND v.asset_id = v.base_id
           JOIN llm967g_look l ON l.bush = v.bush AND l.state = s.state
           JOIN asset_state_tag t ON t.state_id = s.id
          WHERE l.tags IS NOT NULL)
        UNION ALL
        (SELECT s.id, t.tag
           FROM asset_state s
           JOIN llm967g_asset v ON v.asset_id = s.asset_id AND v.asset_id = v.base_id
           JOIN llm967g_look l ON l.bush = v.bush AND l.state = s.state
           JOIN asset_state_tag t ON t.state_id = s.id
          WHERE l.tags IS NOT NULL
         EXCEPT
         SELECT s.id, t.tag
           FROM asset_state s
           JOIN llm967g_asset v ON v.asset_id = s.asset_id AND v.asset_id = v.base_id
           JOIN llm967g_look l ON l.bush = v.bush AND l.state = s.state
           CROSS JOIN LATERAL unnest(l.tags) t(tag)
          WHERE l.tags IS NOT NULL)
    ) diff;
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-967g: % bush winter state tags differ from the authored tags', bad;
    END IF;

    -- The copies carry exactly their bush's tags, state by state.
    SELECT count(*) INTO bad FROM (
        (SELECT v.asset_id, ms.state, t.tag
           FROM llm967g_asset v
           JOIN asset_state ms ON ms.asset_id = v.base_id
           JOIN asset_state_tag t ON t.state_id = ms.id
          WHERE v.asset_id <> v.base_id
         EXCEPT
         SELECT s.asset_id, s.state, t.tag
           FROM asset_state s
           JOIN asset_state_tag t ON t.state_id = s.id
           JOIN llm967g_asset v ON v.asset_id = s.asset_id AND v.asset_id <> v.base_id)
        UNION ALL
        (SELECT s.asset_id, s.state, t.tag
           FROM asset_state s
           JOIN asset_state_tag t ON t.state_id = s.id
           JOIN llm967g_asset v ON v.asset_id = s.asset_id AND v.asset_id <> v.base_id
         EXCEPT
         SELECT v.asset_id, ms.state, t.tag
           FROM llm967g_asset v
           JOIN asset_state ms ON ms.asset_id = v.base_id
           JOIN asset_state_tag t ON t.state_id = ms.id
          WHERE v.asset_id <> v.base_id)
    ) diff;
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-967g: % tags differ between a bush and its copies', bad;
    END IF;

    -- Each pinned bush that exists, and is still on its own kind's assets, is
    -- on its variant.
    SELECT count(*) INTO bad
      FROM llm967g_place p
      JOIN village_object o ON o.id = p.object_id
      JOIN llm967g_asset v ON v.bush = p.bush AND v.variant = p.variant
     WHERE o.asset_id <> v.asset_id
       AND o.asset_id IN (SELECT asset_id FROM llm967g_asset WHERE bush = p.bush);
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-967g: % placed bushes are not on their variant', bad;
    END IF;

    -- The copies are their bush in every copied column (null-safe).
    SELECT count(*) INTO bad
      FROM llm967g_asset v
      JOIN asset a ON a.id = v.asset_id
      JOIN asset m ON m.id = v.base_id
     WHERE v.asset_id <> v.base_id
       AND (a.name, a.category, a.default_state, a.anchor_x, a.anchor_y, a.layer, a.pack_id, a.interior, a.fits_slot,
            a.source_file, a.rotation_algo, a.transition_spread_seconds, a.is_obstacle, a.is_passage, a.z_index,
            a.footprint_left, a.footprint_right, a.footprint_top, a.footprint_bottom, a.door_offset_x, a.door_offset_y,
            a.visible_when_inside, a.stand_offset_x, a.stand_offset_y, a.occupied_min_count, a.occupied_night_only,
            a.render_scale)
           IS DISTINCT FROM
           (v.name, m.category, m.default_state, m.anchor_x, m.anchor_y, m.layer, m.pack_id, m.interior, m.fits_slot,
            m.source_file, m.rotation_algo, m.transition_spread_seconds, m.is_obstacle, m.is_passage, m.z_index,
            m.footprint_left, m.footprint_right, m.footprint_top, m.footprint_bottom, m.door_offset_x, m.door_offset_y,
            m.visible_when_inside, m.stand_offset_x, m.stand_offset_y, m.occupied_min_count, m.occupied_night_only,
            m.render_scale);
    IF bad > 0 OR (SELECT count(*) FROM asset a JOIN llm967g_asset v ON v.asset_id = a.id)
                  <> (SELECT count(*) FROM llm967g_asset) THEN
        RAISE EXCEPTION 'LLM-967g: % bush copies differ from their bush''s asset columns', bad;
    END IF;

    -- And fruit like it: the same rest-default rows, both directions.
    SELECT count(*) INTO bad FROM (
        (SELECT v.asset_id, d.attribute, d.amount, d.available_quantity, d.max_quantity, d.refresh_mode,
                d.refresh_period_hours, d.dwell_amount, d.dwell_period_minutes, d.gather_item
           FROM llm967g_asset v JOIN asset_refresh_default d ON d.asset_id = v.base_id
          WHERE v.asset_id <> v.base_id
         EXCEPT
         SELECT d.asset_id, d.attribute, d.amount, d.available_quantity, d.max_quantity, d.refresh_mode,
                d.refresh_period_hours, d.dwell_amount, d.dwell_period_minutes, d.gather_item
           FROM asset_refresh_default d JOIN llm967g_asset v ON v.asset_id = d.asset_id AND v.asset_id <> v.base_id)
        UNION ALL
        (SELECT d.asset_id, d.attribute, d.amount, d.available_quantity, d.max_quantity, d.refresh_mode,
                d.refresh_period_hours, d.dwell_amount, d.dwell_period_minutes, d.gather_item
           FROM asset_refresh_default d JOIN llm967g_asset v ON v.asset_id = d.asset_id AND v.asset_id <> v.base_id
         EXCEPT
         SELECT v.asset_id, d.attribute, d.amount, d.available_quantity, d.max_quantity, d.refresh_mode,
                d.refresh_period_hours, d.dwell_amount, d.dwell_period_minutes, d.gather_item
           FROM llm967g_asset v JOIN asset_refresh_default d ON d.asset_id = v.base_id
          WHERE v.asset_id <> v.base_id)
    ) diff;
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-967g: % rest-default rows differ between a bush and its copies', bad;
    END IF;
END $$;

COMMIT;
