-- LLM-967: painted apple trees, three of them, with the light-wind rustle.
--
-- The Apple Tree drew Mana Seed art, whose licence forbids use alongside AI.
-- tools/tree-art now paints it as it paints the maples, chestnuts and
-- birches: client/assets/trees/apple-1.png, -2, -3, three trees grown from
-- different seeds. This migration sorts after LLM-967c-chestnut-birch
-- (salem migrations run in filename order).
--
-- Sheet layout (build.py, the apple's own frame): one row per look, top to
-- bottom: berries (summer, apples on), bare (summer, picked), autumn-berries,
-- autumn-bare; a row is 36 frames of 48x64, left to right, played at 6 fps
-- (all four looks are leafy, so all rustle). The picked looks are the same
-- tree without its apples.
--
-- WHAT this does, when the Apple Tree exists (a fresh database has no
-- catalog; then it does nothing):
--   1. Its four states point at apple-1.png in that layout. Their tags
--      (berries / bare, season-autumn) are untouched, so picking still
--      switches a tree between its berries and bare states.
--   2. Apple Tree 2 and Apple Tree 3 (pinned ids) are its copies: every
--      asset column but id and name, the four states on apple-2/-3, the same
--      tags, and its rest default (asset_refresh_default: three apples to
--      pick, refilled weekly), so a new placement fruits alike.
--   3. The 20 placed apple trees (pinned ids) are spread over the three, 7/7/6,
--      chosen so that every tree's nearest apple tree is a different one. Their
--      current state and object_refresh rows (apples left) stay with them.
-- Then every one of those must hold exactly, or the migration fails.
--
-- ENGINE-OWNED data (catalog and village_object), read at boot; deploy.sh
-- runs stop -> migrate -> start. Convergent: rerunning ends in the same place.

BEGIN;

DO $$
DECLARE
    apple CONSTANT uuid := '019e5f00-c401-7a10-9e00-000000000623';
    apple2 CONSTANT uuid := '48f8b1e5-a9e6-45d7-87f9-acbbf83fcc61';
    apple3 CONSTANT uuid := 'e8d24766-7ef7-4380-8ff4-0e4de2d9cb05';
    bad integer;
BEGIN
    IF NOT EXISTS (SELECT 1 FROM asset WHERE id = apple) THEN
        RETURN;
    END IF;

    -- The three assets and their sheets.
    CREATE TEMP TABLE llm967d_asset (asset_id uuid PRIMARY KEY, variant integer NOT NULL, name text NOT NULL, sheet text NOT NULL) ON COMMIT DROP;
    INSERT INTO llm967d_asset VALUES
        (apple,  1, 'Apple Tree',   'res://assets/trees/apple-1.png'),
        (apple2, 2, 'Apple Tree 2', 'res://assets/trees/apple-2.png'),
        (apple3, 3, 'Apple Tree 3', 'res://assets/trees/apple-3.png');

    -- Each look's row: 36 frames of 48x64 at 6 fps.
    CREATE TEMP TABLE llm967d_look (state text PRIMARY KEY, src_y integer NOT NULL) ON COMMIT DROP;
    INSERT INTO llm967d_look VALUES
        ('berries',          0),
        ('bare',            64),
        ('autumn-berries', 128),
        ('autumn-bare',    192);

    -- 2. The two new assets: the Apple Tree's columns, their own id and name.
    INSERT INTO asset (id, name, category, default_state, anchor_x, anchor_y, layer, pack_id, interior, fits_slot,
                       source_file, rotation_algo, transition_spread_seconds, is_obstacle, is_passage, z_index,
                       footprint_left, footprint_right, footprint_top, footprint_bottom, door_offset_x, door_offset_y,
                       visible_when_inside, stand_offset_x, stand_offset_y, occupied_min_count, occupied_night_only, render_scale)
    SELECT v.asset_id, v.name, m.category, m.default_state, m.anchor_x, m.anchor_y, m.layer, m.pack_id, m.interior, m.fits_slot,
           m.source_file, m.rotation_algo, m.transition_spread_seconds, m.is_obstacle, m.is_passage, m.z_index,
           m.footprint_left, m.footprint_right, m.footprint_top, m.footprint_bottom, m.door_offset_x, m.door_offset_y,
           m.visible_when_inside, m.stand_offset_x, m.stand_offset_y, m.occupied_min_count, m.occupied_night_only, m.render_scale
      FROM asset m, llm967d_asset v
     WHERE m.id = apple AND v.asset_id <> apple
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

    -- 1 and 2. Every look of every apple tree on its sheet, in its row.
    INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
    SELECT v.asset_id, l.state, v.sheet, 0, l.src_y, 48, 64, 36, 6
      FROM llm967d_asset v CROSS JOIN llm967d_look l
    ON CONFLICT (asset_id, state) DO UPDATE
       SET sheet = EXCLUDED.sheet, src_x = EXCLUDED.src_x, src_y = EXCLUDED.src_y, src_w = EXCLUDED.src_w,
           src_h = EXCLUDED.src_h, frame_count = EXCLUDED.frame_count, frame_rate = EXCLUDED.frame_rate;

    -- The Apple Tree's tags (fruit state and season) on the same states of
    -- the new assets.
    INSERT INTO asset_state_tag (state_id, tag)
    SELECT vs.id, t.tag
      FROM asset_state ms
      JOIN asset_state_tag t ON t.state_id = ms.id
      JOIN asset_state vs ON vs.state = ms.state AND vs.asset_id IN (apple2, apple3)
     WHERE ms.asset_id = apple
    ON CONFLICT DO NOTHING;

    -- Its rest default, for new placements of the new assets: the variants'
    -- rows are replaced by a fresh copy, so a rerun repairs a hand edit.
    DELETE FROM asset_refresh_default WHERE asset_id IN (apple2, apple3);
    INSERT INTO asset_refresh_default (asset_id, attribute, amount, available_quantity, max_quantity, refresh_mode,
                                       refresh_period_hours, dwell_amount, dwell_period_minutes, gather_item)
    SELECT v.asset_id, d.attribute, d.amount, d.available_quantity, d.max_quantity, d.refresh_mode,
           d.refresh_period_hours, d.dwell_amount, d.dwell_period_minutes, d.gather_item
      FROM asset_refresh_default d, llm967d_asset v
     WHERE d.asset_id = apple AND v.asset_id <> apple;

    -- 3. The placed apple trees, each to its variant. The orchard, by
    -- position, west to east.
    CREATE TEMP TABLE llm967d_place (object_id uuid PRIMARY KEY, variant integer NOT NULL) ON COMMIT DROP;
    INSERT INTO llm967d_place VALUES
        ('019da68b-e156-776e-a0e4-46c8c3b2f2f5', 2),  -- 3057, -527
        ('019da68c-cb14-7f28-a7c3-ff971e40feb7', 2),  -- 3059, -103
        ('019da68c-335c-770e-88d5-166179ad0f0f', 1),  -- 3062, -384
        ('019da68c-94ff-71d8-be5d-efcbed2fc4ae', 3),  -- 3062, -244
        ('019da68e-6c2d-7c41-8ab5-8f39f4426f55', 2),  -- 3262, -412
        ('019da68e-86dc-78b0-9125-ea4aa5ce392a', 1),  -- 3283, -259
        ('019da68e-a71b-7878-b122-f938c7f1db4a', 3),  -- 3285, -113
        ('019da68e-1ac4-75f4-9164-ad6bfb303a5d', 3),  -- 3288, -567
        ('019da68e-dd93-7209-9614-ba403ade9640', 2),  -- 3483, -412
        ('019da68f-0487-7574-a120-4161cc983632', 3),  -- 3486, -281
        ('019da68f-2271-76e1-ac8c-88af8e53c194', 2),  -- 3486, -113
        ('019da68d-f208-77af-b331-b64de9723109', 1),  -- 3496, -557
        ('019da68f-7458-7c2f-a67e-822964956bac', 1),  -- 3694, -382
        ('019da68f-a92e-7120-8527-c5bc88ed56fa', 2),  -- 3707, -274
        ('019da68e-39fe-7cf2-bc72-39d0d02a323b', 3),  -- 3712, -560
        ('019da68f-dee7-74a3-bb0b-2da1226b906a', 1),  -- 3719, -88
        ('019da690-07a2-7a66-8d97-db76d77d5648', 3),  -- 3885, -377
        ('019da68d-a3ce-723a-91c6-3381b169eee2', 1),  -- 3905, -535
        ('019da690-4df2-75ce-bc2a-30180d5a5132', 2),  -- 3910, -78
        ('019da690-2863-7213-bb2e-76db22f5ddb1', 1);  -- 3913, -226

    -- Only a tree still on an apple asset moves (a placed id hand-switched to
    -- some other asset is left alone).
    UPDATE village_object o
       SET asset_id = v.asset_id
      FROM llm967d_place p
      JOIN llm967d_asset v ON v.variant = p.variant
     WHERE o.id = p.object_id
       AND o.asset_id IN (apple, apple2, apple3);

    -- Every apple tree has exactly the four states, with exactly these values.
    SELECT count(*) INTO bad FROM (
        (SELECT v.asset_id, l.state, v.sheet, 0 AS src_x, l.src_y, 48 AS src_w, 64 AS src_h, 36 AS frame_count, 6::double precision AS frame_rate
           FROM llm967d_asset v CROSS JOIN llm967d_look l
         EXCEPT
         SELECT s.asset_id, s.state, s.sheet, s.src_x, s.src_y, s.src_w, s.src_h, s.frame_count, s.frame_rate
           FROM asset_state s JOIN llm967d_asset v ON v.asset_id = s.asset_id)
        UNION ALL
        (SELECT s.asset_id, s.state, s.sheet, s.src_x, s.src_y, s.src_w, s.src_h, s.frame_count, s.frame_rate
           FROM asset_state s JOIN llm967d_asset v ON v.asset_id = s.asset_id
         EXCEPT
         SELECT v.asset_id, l.state, v.sheet, 0, l.src_y, 48, 64, 36, 6::double precision
           FROM llm967d_asset v CROSS JOIN llm967d_look l)
    ) diff;
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-967d: % apple tree state rows differ from the authored values', bad;
    END IF;

    -- The new apple trees carry exactly the Apple Tree's tags, state by state.
    SELECT count(*) INTO bad FROM (
        (SELECT v.asset_id, ms.state, t.tag
           FROM asset_state ms JOIN asset_state_tag t ON t.state_id = ms.id, llm967d_asset v
          WHERE ms.asset_id = apple AND v.asset_id <> apple
         EXCEPT
         SELECT s.asset_id, s.state, t.tag
           FROM asset_state s JOIN asset_state_tag t ON t.state_id = s.id
          WHERE s.asset_id IN (apple2, apple3))
        UNION ALL
        (SELECT s.asset_id, s.state, t.tag
           FROM asset_state s JOIN asset_state_tag t ON t.state_id = s.id
          WHERE s.asset_id IN (apple2, apple3)
         EXCEPT
         SELECT v.asset_id, ms.state, t.tag
           FROM asset_state ms JOIN asset_state_tag t ON t.state_id = ms.id, llm967d_asset v
          WHERE ms.asset_id = apple AND v.asset_id <> apple)
    ) diff;
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-967d: % tags differ between the Apple Tree and its variants', bad;
    END IF;

    -- Each pinned apple tree that exists, and is still an apple tree, is on its variant.
    SELECT count(*) INTO bad
      FROM llm967d_place p
      JOIN village_object o ON o.id = p.object_id
      JOIN llm967d_asset v ON v.variant = p.variant
     WHERE o.asset_id <> v.asset_id
       AND o.asset_id IN (apple, apple2, apple3);
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-967d: % placed apple trees are not on their variant', bad;
    END IF;

    -- The new apple trees are the Apple Tree in every copied column (null-safe).
    SELECT count(*) INTO bad
      FROM asset a, asset m
     WHERE m.id = apple AND a.id IN (apple2, apple3)
       AND (a.category, a.default_state, a.anchor_x, a.anchor_y, a.layer, a.pack_id, a.interior, a.fits_slot,
            a.source_file, a.rotation_algo, a.transition_spread_seconds, a.is_obstacle, a.is_passage, a.z_index,
            a.footprint_left, a.footprint_right, a.footprint_top, a.footprint_bottom, a.door_offset_x, a.door_offset_y,
            a.visible_when_inside, a.stand_offset_x, a.stand_offset_y, a.occupied_min_count, a.occupied_night_only,
            a.render_scale)
           IS DISTINCT FROM
           (m.category, m.default_state, m.anchor_x, m.anchor_y, m.layer, m.pack_id, m.interior, m.fits_slot,
            m.source_file, m.rotation_algo, m.transition_spread_seconds, m.is_obstacle, m.is_passage, m.z_index,
            m.footprint_left, m.footprint_right, m.footprint_top, m.footprint_bottom, m.door_offset_x, m.door_offset_y,
            m.visible_when_inside, m.stand_offset_x, m.stand_offset_y, m.occupied_min_count, m.occupied_night_only,
            m.render_scale);
    IF bad > 0 OR (SELECT count(*) FROM asset WHERE id IN (apple2, apple3)) <> 2 THEN
        RAISE EXCEPTION 'LLM-967d: % apple tree variants differ from the Apple Tree''s asset columns', bad;
    END IF;

    -- And fruit like it: the same refresh-default rows, both directions.
    SELECT count(*) INTO bad FROM (
        (SELECT v.asset_id, d.attribute, d.amount, d.available_quantity, d.max_quantity, d.refresh_mode,
                d.refresh_period_hours, d.dwell_amount, d.dwell_period_minutes, d.gather_item
           FROM asset_refresh_default d, llm967d_asset v
          WHERE d.asset_id = apple AND v.asset_id <> apple
         EXCEPT
         SELECT asset_id, attribute, amount, available_quantity, max_quantity, refresh_mode,
                refresh_period_hours, dwell_amount, dwell_period_minutes, gather_item
           FROM asset_refresh_default WHERE asset_id IN (apple2, apple3))
        UNION ALL
        (SELECT asset_id, attribute, amount, available_quantity, max_quantity, refresh_mode,
                refresh_period_hours, dwell_amount, dwell_period_minutes, gather_item
           FROM asset_refresh_default WHERE asset_id IN (apple2, apple3)
         EXCEPT
         SELECT v.asset_id, d.attribute, d.amount, d.available_quantity, d.max_quantity, d.refresh_mode,
                d.refresh_period_hours, d.dwell_amount, d.dwell_period_minutes, d.gather_item
           FROM asset_refresh_default d, llm967d_asset v
          WHERE d.asset_id = apple AND v.asset_id <> apple)
    ) diff;
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-967d: % rest-default rows differ between the Apple Tree and its variants', bad;
    END IF;
END $$;

COMMIT;
