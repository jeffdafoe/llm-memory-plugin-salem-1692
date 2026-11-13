-- LLM-967: painted chestnuts and birches, three of each, with the light-wind
-- rustle on the leafy looks.
--
-- The Chestnut Tree and the Birch Tree drew Mana Seed art, whose licence
-- forbids use alongside AI. tools/tree-art now paints them as it paints the
-- maples (LLM-967b-maple-variants): client/assets/trees/chestnut-1.png, -2,
-- -3 and birch-1.png, -2, -3, three trees per species grown from different
-- seeds. This migration sorts after LLM-967b (salem migrations run in
-- filename order).
--
-- Sheet layout (build.py): one row per look, top to bottom in LOOKS order
-- (default, autumn-clean, autumn-leaves, autumn-bare, winter-clean,
-- winter-leaves, winter-snowy-top, winter-snowy); a row's frames run left
-- to right, 80x112 each. The three leafy looks have 36 frames at 6 fps, the
-- others one.
--
-- WHAT this does, for each of the two trees that exists (a fresh database
-- has no catalog; then it does nothing):
--   1. Its eight states point at <species>-1.png in that layout.
--   2. <Tree> 2 and <Tree> 3 (pinned ids) are its copies: every asset column
--      but id and name, the eight states on <species>-2/-3, the same season
--      tags, and its rest default (asset_refresh_default; today neither tree
--      has one, so the copy is empty), so a new placement behaves alike.
--   3. Its placed trees (pinned ids: 20 chestnuts, 13 birches) are spread
--      over the three, chosen so that every tree's nearest tree of its kind
--      is a different one. Their object_refresh rows stay with them.
-- Then every one of those must hold exactly, or the migration fails.
--
-- ENGINE-OWNED data (catalog and village_object), read at boot; deploy.sh
-- runs stop -> migrate -> start. Convergent: rerunning ends in the same place.

BEGIN;

DO $$
DECLARE
    chestnut CONSTANT uuid := '3879a83f-d32b-4b48-a108-f755c212cf0d';
    chestnut2 CONSTANT uuid := '30af7fb9-21cc-40b6-887f-1cb97b4fab86';
    chestnut3 CONSTANT uuid := 'd69cbe25-14a1-4da7-9e48-491be7bf9250';
    birch CONSTANT uuid := '1d013a1d-95c3-4d17-86e4-be18ea733528';
    birch2 CONSTANT uuid := 'e5f00663-97a0-44e8-9aa5-75149dde0c5c';
    birch3 CONSTANT uuid := '9a61e677-a795-48e5-947d-e8ba52aacf5e';
    bad integer;
BEGIN
    -- Every tree of a species that exists: the base (variant 1) and its two
    -- copies, with their sheets.
    CREATE TEMP TABLE llm967c_asset (asset_id uuid PRIMARY KEY, base_id uuid NOT NULL, variant integer NOT NULL,
                                     name text NOT NULL, sheet text NOT NULL) ON COMMIT DROP;
    INSERT INTO llm967c_asset
    SELECT v.* FROM (VALUES
        (chestnut,  chestnut, 1, 'Chestnut Tree',   'res://assets/trees/chestnut-1.png'),
        (chestnut2, chestnut, 2, 'Chestnut Tree 2', 'res://assets/trees/chestnut-2.png'),
        (chestnut3, chestnut, 3, 'Chestnut Tree 3', 'res://assets/trees/chestnut-3.png'),
        (birch,     birch,    1, 'Birch Tree',      'res://assets/trees/birch-1.png'),
        (birch2,    birch,    2, 'Birch Tree 2',    'res://assets/trees/birch-2.png'),
        (birch3,    birch,    3, 'Birch Tree 3',    'res://assets/trees/birch-3.png')
    ) AS v(asset_id, base_id, variant, name, sheet)
     WHERE EXISTS (SELECT 1 FROM asset WHERE id = v.base_id);
    IF NOT EXISTS (SELECT 1 FROM llm967c_asset) THEN
        RETURN;
    END IF;

    -- Each look's row and frames.
    CREATE TEMP TABLE llm967c_look (state text PRIMARY KEY, src_y integer NOT NULL, frame_count integer NOT NULL, frame_rate double precision NOT NULL) ON COMMIT DROP;
    INSERT INTO llm967c_look VALUES
        ('default',            0, 36, 6),
        ('autumn-clean',     112, 36, 6),
        ('autumn-leaves',    224, 36, 6),
        ('autumn-bare',      336,  1, 0),
        ('winter-clean',     448,  1, 0),
        ('winter-leaves',    560,  1, 0),
        ('winter-snowy-top', 672,  1, 0),
        ('winter-snowy',     784,  1, 0);

    -- 2. The new assets: their base's columns, their own id and name.
    INSERT INTO asset (id, name, category, default_state, anchor_x, anchor_y, layer, pack_id, interior, fits_slot,
                       source_file, rotation_algo, transition_spread_seconds, is_obstacle, is_passage, z_index,
                       footprint_left, footprint_right, footprint_top, footprint_bottom, door_offset_x, door_offset_y,
                       visible_when_inside, stand_offset_x, stand_offset_y, occupied_min_count, occupied_night_only, render_scale)
    SELECT v.asset_id, v.name, m.category, m.default_state, m.anchor_x, m.anchor_y, m.layer, m.pack_id, m.interior, m.fits_slot,
           m.source_file, m.rotation_algo, m.transition_spread_seconds, m.is_obstacle, m.is_passage, m.z_index,
           m.footprint_left, m.footprint_right, m.footprint_top, m.footprint_bottom, m.door_offset_x, m.door_offset_y,
           m.visible_when_inside, m.stand_offset_x, m.stand_offset_y, m.occupied_min_count, m.occupied_night_only, m.render_scale
      FROM llm967c_asset v JOIN asset m ON m.id = v.base_id
     WHERE v.variant <> 1
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

    -- 1 and 2. Every look of every tree on its sheet, in its row.
    INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
    SELECT v.asset_id, l.state, v.sheet, 0, l.src_y, 80, 112, l.frame_count, l.frame_rate
      FROM llm967c_asset v CROSS JOIN llm967c_look l
    ON CONFLICT (asset_id, state) DO UPDATE
       SET sheet = EXCLUDED.sheet, src_x = EXCLUDED.src_x, src_y = EXCLUDED.src_y, src_w = EXCLUDED.src_w,
           src_h = EXCLUDED.src_h, frame_count = EXCLUDED.frame_count, frame_rate = EXCLUDED.frame_rate;

    -- The base's season tags on the same states of its copies.
    INSERT INTO asset_state_tag (state_id, tag)
    SELECT vs.id, t.tag
      FROM llm967c_asset v
      JOIN asset_state ms ON ms.asset_id = v.base_id
      JOIN asset_state_tag t ON t.state_id = ms.id
      JOIN asset_state vs ON vs.asset_id = v.asset_id AND vs.state = ms.state
     WHERE v.variant <> 1
    ON CONFLICT DO NOTHING;

    -- Its rest default, for new placements of the copies: the copies' rows
    -- are replaced by a fresh copy, so a rerun repairs a hand edit.
    DELETE FROM asset_refresh_default WHERE asset_id IN (SELECT asset_id FROM llm967c_asset WHERE variant <> 1);
    INSERT INTO asset_refresh_default (asset_id, attribute, amount, available_quantity, max_quantity, refresh_mode,
                                       refresh_period_hours, dwell_amount, dwell_period_minutes, gather_item)
    SELECT v.asset_id, d.attribute, d.amount, d.available_quantity, d.max_quantity, d.refresh_mode,
           d.refresh_period_hours, d.dwell_amount, d.dwell_period_minutes, d.gather_item
      FROM asset_refresh_default d JOIN llm967c_asset v ON v.base_id = d.asset_id
     WHERE v.variant <> 1;

    -- 3. The placed trees, each to its variant. Ordered by position, west to east.
    CREATE TEMP TABLE llm967c_place (object_id uuid PRIMARY KEY, base_id uuid NOT NULL, variant integer NOT NULL) ON COMMIT DROP;
    INSERT INTO llm967c_place VALUES
        ('019d79ef-d9de-74cf-a71a-21b275605587', chestnut, 2),  -- 78, 493
        ('019d79ef-d9de-7839-acd6-d17551173d31', chestnut, 2),  -- 87, 684
        ('019d79ef-d9dc-7651-ad9e-0e757cd32cc4', chestnut, 1),  -- 141, 597
        ('019d79ef-d9db-79e1-996e-b666af94af4f', chestnut, 2),  -- 146, 1141
        ('019d79ef-d9db-7897-8950-30cabd7f5305', chestnut, 1),  -- 181, 298
        ('019d79ef-d9db-781f-8aa2-3e3dc3567be3', chestnut, 2),  -- 185, 234
        ('019d79ef-d9da-7c69-9ab5-01e3dbc194c1', chestnut, 3),  -- 249, 170
        ('019d79ef-d9db-706f-bcd1-a4c7c9ba79d3', chestnut, 1),  -- 270, 1132
        ('019dc1c3-9b33-7028-b2e1-a62753430743', chestnut, 1),  -- 326, -116
        ('019d79ef-d9dc-7029-85d7-9780383c82f1', chestnut, 2),  -- 335, 52
        ('019d79ef-d9dc-79ea-9870-8fae716e8212', chestnut, 3),  -- 409, -8
        ('019d79ef-d9db-767b-9f75-43ed6b51b10f', chestnut, 1),  -- 411, 168
        ('019d79ef-d9dc-7402-8b39-2c105467f25f', chestnut, 3),  -- 522, 1358
        ('019d79ef-d9dc-7992-ae2b-27f280245e7c', chestnut, 2),  -- 529, 82
        ('019d79ef-d9dc-7303-8dc6-58fbca68001e', chestnut, 2),  -- 734, 1391
        ('019d79ef-d9dc-70f1-90b1-32aed4730d9c', chestnut, 3),  -- 928, 1433
        ('019d79ef-d9dd-7cfd-bedb-ebdcfe69f950', chestnut, 3),  -- 1098, 86
        ('019d79ef-d9dd-76d5-97a9-c234946b4232', chestnut, 1),  -- 1586, 1394
        ('019d79ef-d9dd-7992-80c5-a4f55433f595', chestnut, 3),  -- 1785, 1397
        ('019d79ef-d9dd-7147-b4c3-646d4d751b37', chestnut, 1),  -- 1864, 43
        ('019d79ef-d9dc-73de-b002-fd9e801d0c7c', birch, 1),  -- 43, 1360
        ('019d79ef-d9dc-721d-8e35-45a982d10a10', birch, 3),  -- 47, 82
        ('019d79ef-d9de-7cdb-b886-942b883d07a7', birch, 1),  -- 74, 398
        ('019dc1c3-c968-7fd1-8c26-545911b8dd13', birch, 1),  -- 236, -153
        ('019d79ef-d9db-7692-8249-ab2f09e503f9', birch, 2),  -- 277, 1206
        ('019dc1c4-23ac-7b5f-9564-e391c35b6b3f', birch, 2),  -- 350, -37
        ('019d79ef-d9dc-7e57-98c6-e3f46d41e463', birch, 3),  -- 520, 1186
        ('019d79ef-d9dc-7799-b2d4-d980d04b46f4', birch, 3),  -- 729, 46
        ('019d79ef-d9dc-745e-8e56-0dff2a7a5b9d', birch, 1),  -- 819, 48
        ('019d79ef-d9dd-765f-947e-5830e575c8b5', birch, 2),  -- 1203, 1431
        ('019d79ef-d9dd-7685-b4ab-1c05ac91e4e4', birch, 3),  -- 1496, 1395
        ('019d98b4-a16c-73c3-a664-b1859a13b940', birch, 1),  -- 2159, 1151
        ('019d98b4-c182-7956-91e0-7f2af08ec78c', birch, 2);  -- 2703, 1181

    -- Only a tree still of its own kind moves (a placed id hand-switched to
    -- some other asset is left alone).
    UPDATE village_object o
       SET asset_id = v.asset_id
      FROM llm967c_place p
      JOIN llm967c_asset v ON v.base_id = p.base_id AND v.variant = p.variant
     WHERE o.id = p.object_id
       AND o.asset_id IN (SELECT asset_id FROM llm967c_asset WHERE base_id = p.base_id);

    -- Every tree has exactly the eight states, with exactly these values.
    SELECT count(*) INTO bad FROM (
        (SELECT v.asset_id, l.state, v.sheet, 0 AS src_x, l.src_y, 80 AS src_w, 112 AS src_h, l.frame_count, l.frame_rate
           FROM llm967c_asset v CROSS JOIN llm967c_look l
         EXCEPT
         SELECT s.asset_id, s.state, s.sheet, s.src_x, s.src_y, s.src_w, s.src_h, s.frame_count, s.frame_rate
           FROM asset_state s JOIN llm967c_asset v ON v.asset_id = s.asset_id)
        UNION ALL
        (SELECT s.asset_id, s.state, s.sheet, s.src_x, s.src_y, s.src_w, s.src_h, s.frame_count, s.frame_rate
           FROM asset_state s JOIN llm967c_asset v ON v.asset_id = s.asset_id
         EXCEPT
         SELECT v.asset_id, l.state, v.sheet, 0, l.src_y, 80, 112, l.frame_count, l.frame_rate
           FROM llm967c_asset v CROSS JOIN llm967c_look l)
    ) diff;
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-967c: % chestnut/birch state rows differ from the authored values', bad;
    END IF;

    -- The copies carry exactly their base's tags, state by state.
    SELECT count(*) INTO bad FROM (
        (SELECT v.asset_id, ms.state, t.tag
           FROM llm967c_asset v
           JOIN asset_state ms ON ms.asset_id = v.base_id
           JOIN asset_state_tag t ON t.state_id = ms.id
          WHERE v.variant <> 1
         EXCEPT
         SELECT s.asset_id, s.state, t.tag
           FROM asset_state s JOIN asset_state_tag t ON t.state_id = s.id
          WHERE s.asset_id IN (SELECT asset_id FROM llm967c_asset WHERE variant <> 1))
        UNION ALL
        (SELECT s.asset_id, s.state, t.tag
           FROM asset_state s JOIN asset_state_tag t ON t.state_id = s.id
          WHERE s.asset_id IN (SELECT asset_id FROM llm967c_asset WHERE variant <> 1)
         EXCEPT
         SELECT v.asset_id, ms.state, t.tag
           FROM llm967c_asset v
           JOIN asset_state ms ON ms.asset_id = v.base_id
           JOIN asset_state_tag t ON t.state_id = ms.id
          WHERE v.variant <> 1)
    ) diff;
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-967c: % season tags differ between a tree and its copies', bad;
    END IF;

    -- Each pinned tree that exists, and is still of its kind, is on its variant.
    SELECT count(*) INTO bad
      FROM llm967c_place p
      JOIN village_object o ON o.id = p.object_id
      JOIN llm967c_asset v ON v.base_id = p.base_id AND v.variant = p.variant
     WHERE o.asset_id <> v.asset_id
       AND o.asset_id IN (SELECT asset_id FROM llm967c_asset WHERE base_id = p.base_id);
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-967c: % placed chestnuts/birches are not on their variant', bad;
    END IF;

    -- The copies are their base in every copied column (null-safe).
    SELECT count(*) INTO bad
      FROM llm967c_asset v JOIN asset a ON a.id = v.asset_id JOIN asset m ON m.id = v.base_id
     WHERE v.variant <> 1
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
    IF bad > 0 OR (SELECT count(*) FROM asset WHERE id IN (SELECT asset_id FROM llm967c_asset))
                  <> (SELECT count(*) FROM llm967c_asset) THEN
        RAISE EXCEPTION 'LLM-967c: % chestnut/birch copies differ from their base''s asset columns', bad;
    END IF;

    -- And rest like it: the same refresh-default rows, both directions.
    SELECT count(*) INTO bad FROM (
        (SELECT v.asset_id, d.attribute, d.amount, d.available_quantity, d.max_quantity, d.refresh_mode,
                d.refresh_period_hours, d.dwell_amount, d.dwell_period_minutes, d.gather_item
           FROM asset_refresh_default d JOIN llm967c_asset v ON v.base_id = d.asset_id
          WHERE v.variant <> 1
         EXCEPT
         SELECT asset_id, attribute, amount, available_quantity, max_quantity, refresh_mode,
                refresh_period_hours, dwell_amount, dwell_period_minutes, gather_item
           FROM asset_refresh_default
          WHERE asset_id IN (SELECT asset_id FROM llm967c_asset WHERE variant <> 1))
        UNION ALL
        (SELECT asset_id, attribute, amount, available_quantity, max_quantity, refresh_mode,
                refresh_period_hours, dwell_amount, dwell_period_minutes, gather_item
           FROM asset_refresh_default
          WHERE asset_id IN (SELECT asset_id FROM llm967c_asset WHERE variant <> 1)
         EXCEPT
         SELECT v.asset_id, d.attribute, d.amount, d.available_quantity, d.max_quantity, d.refresh_mode,
                d.refresh_period_hours, d.dwell_amount, d.dwell_period_minutes, d.gather_item
           FROM asset_refresh_default d JOIN llm967c_asset v ON v.base_id = d.asset_id
          WHERE v.variant <> 1)
    ) diff;
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-967c: % rest-default rows differ between a tree and its copies', bad;
    END IF;
END $$;

COMMIT;
