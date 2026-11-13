-- LLM-967: three painted maples, and a light-wind rustle on the leafy looks.
--
-- All 24 placed maples drew one sprite, so the same fork showed through the
-- same gap in every crown. tools/tree-art now paints three maples from
-- different seeds into client/assets/trees/maple-1.png, -2, -3, and the
-- leafy looks are animated: a 36-frame loop at 6 fps that is still but for
-- one puff (leaf clusters turning their pale undersides, the crown leaning a
-- pixel by leaf clusters). The client starts every placed tree at a random
-- frame and speed, so no two rustle together.
--
-- Sheet layout (build.py): one row per look, top to bottom in LOOKS order
-- (default, autumn-clean, autumn-leaves, autumn-bare, winter-clean,
-- winter-leaves, winter-snowy-top, winter-snowy); a row's frames run left
-- to right, 80x112 each. The three leafy looks have 36 frames at 6 fps, the
-- others one.
--
-- WHAT this does, when the Maple Tree exists (a fresh database has no
-- catalog; then it does nothing):
--   1. The Maple Tree's eight states point at maple-1.png in that layout.
--   2. Maple Tree 2 and Maple Tree 3 (pinned ids) are the Maple Tree's copy:
--      every asset column but id and name, the eight states on maple-2/-3,
--      the same season tags, and its rest default (asset_refresh_default:
--      sitting under it eases tiredness), so a new placement behaves alike.
--   3. The 24 placed maples (pinned ids) are spread over the three, 8 each,
--      chosen so that a maple's nearest maple is a different one (23 of 24).
--      Their object_refresh rows stay with them.
-- Then every one of those must hold exactly, or the migration fails.
--
-- ENGINE-OWNED data (catalog and village_object), read at boot; deploy.sh
-- runs stop -> migrate -> start. Convergent: rerunning ends in the same place.

BEGIN;

DO $$
DECLARE
    maple CONSTANT uuid := '2d91c8a9-6501-4e16-873a-4d18bdc6f63e';
    maple2 CONSTANT uuid := 'a0882055-bfb9-4b12-8f63-88ea73dd6e3c';
    maple3 CONSTANT uuid := 'f0f99142-2e0d-4a72-89de-838db17082b1';
    bad integer;
BEGIN
    IF NOT EXISTS (SELECT 1 FROM asset WHERE id = maple) THEN
        RETURN;
    END IF;

    -- The three assets and their sheets.
    CREATE TEMP TABLE llm967v_asset (asset_id uuid PRIMARY KEY, name text NOT NULL, sheet text NOT NULL) ON COMMIT DROP;
    INSERT INTO llm967v_asset VALUES
        (maple,  'Maple Tree',   'res://assets/trees/maple-1.png'),
        (maple2, 'Maple Tree 2', 'res://assets/trees/maple-2.png'),
        (maple3, 'Maple Tree 3', 'res://assets/trees/maple-3.png');

    -- Each look's row and frames.
    CREATE TEMP TABLE llm967v_look (state text PRIMARY KEY, src_y integer NOT NULL, frame_count integer NOT NULL, frame_rate double precision NOT NULL) ON COMMIT DROP;
    INSERT INTO llm967v_look VALUES
        ('default',            0, 36, 6),
        ('autumn-clean',     112, 36, 6),
        ('autumn-leaves',    224, 36, 6),
        ('autumn-bare',      336,  1, 0),
        ('winter-clean',     448,  1, 0),
        ('winter-leaves',    560,  1, 0),
        ('winter-snowy-top', 672,  1, 0),
        ('winter-snowy',     784,  1, 0);

    -- 2. The two new assets: the Maple Tree's columns, their own id and name.
    INSERT INTO asset (id, name, category, default_state, anchor_x, anchor_y, layer, pack_id, interior, fits_slot,
                       source_file, rotation_algo, transition_spread_seconds, is_obstacle, is_passage, z_index,
                       footprint_left, footprint_right, footprint_top, footprint_bottom, door_offset_x, door_offset_y,
                       visible_when_inside, stand_offset_x, stand_offset_y, occupied_min_count, occupied_night_only, render_scale)
    SELECT v.asset_id, v.name, m.category, m.default_state, m.anchor_x, m.anchor_y, m.layer, m.pack_id, m.interior, m.fits_slot,
           m.source_file, m.rotation_algo, m.transition_spread_seconds, m.is_obstacle, m.is_passage, m.z_index,
           m.footprint_left, m.footprint_right, m.footprint_top, m.footprint_bottom, m.door_offset_x, m.door_offset_y,
           m.visible_when_inside, m.stand_offset_x, m.stand_offset_y, m.occupied_min_count, m.occupied_night_only, m.render_scale
      FROM asset m, llm967v_asset v
     WHERE m.id = maple AND v.asset_id <> maple
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

    -- 1 and 2. Every look of every maple on its sheet, in its row.
    INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
    SELECT v.asset_id, l.state, v.sheet, 0, l.src_y, 80, 112, l.frame_count, l.frame_rate
      FROM llm967v_asset v CROSS JOIN llm967v_look l
    ON CONFLICT (asset_id, state) DO UPDATE
       SET sheet = EXCLUDED.sheet, src_x = EXCLUDED.src_x, src_y = EXCLUDED.src_y, src_w = EXCLUDED.src_w,
           src_h = EXCLUDED.src_h, frame_count = EXCLUDED.frame_count, frame_rate = EXCLUDED.frame_rate;

    -- The Maple Tree's season tags on the same states of the new assets.
    INSERT INTO asset_state_tag (state_id, tag)
    SELECT vs.id, t.tag
      FROM asset_state ms
      JOIN asset_state_tag t ON t.state_id = ms.id
      JOIN asset_state vs ON vs.state = ms.state AND vs.asset_id IN (maple2, maple3)
     WHERE ms.asset_id = maple
    ON CONFLICT DO NOTHING;

    -- Its rest default, for new placements of the new assets: the variants'
    -- rows are replaced by a fresh copy, so a rerun repairs a hand edit.
    DELETE FROM asset_refresh_default WHERE asset_id IN (maple2, maple3);
    INSERT INTO asset_refresh_default (asset_id, attribute, amount, available_quantity, max_quantity, refresh_mode,
                                       refresh_period_hours, dwell_amount, dwell_period_minutes, gather_item)
    SELECT v.asset_id, d.attribute, d.amount, d.available_quantity, d.max_quantity, d.refresh_mode,
           d.refresh_period_hours, d.dwell_amount, d.dwell_period_minutes, d.gather_item
      FROM asset_refresh_default d, llm967v_asset v
     WHERE d.asset_id = maple AND v.asset_id <> maple;

    -- 3. The placed maples, each to its maple. Ordered by position, west to east.
    CREATE TEMP TABLE llm967v_place (object_id uuid PRIMARY KEY, variant integer NOT NULL) ON COMMIT DROP;
    INSERT INTO llm967v_place VALUES
        ('019dc1c4-3fdb-7421-8eb7-5d6b9e753075', 3),  -- -122, -52
        ('019dc1c4-02fa-715a-83d2-b856e4f620c5', 2),  -- -27, 7
        ('019dc1c4-61a3-7d14-83ea-0c694a53fc08', 1),  -- -11, 154
        ('019d79ef-d9de-7115-940a-d3afc15bbdf4', 3),  -- 71, 1066
        ('019d79ef-d9de-744a-87ba-831b7d176ad7', 2),  -- 82, 1258
        ('019d79ef-d9de-7db6-9439-770289d881f4', 1),  -- 82, 787
        ('019d79ef-d9db-7e4f-b8dd-20ca12455d26', 1),  -- 152, 1196
        ('019d79ef-d9d9-7014-bf91-a782e5fefc22', 3),  -- 176, 116
        ('019d79ef-d9da-70c3-a13f-ea0a01124471', 1),  -- 182, 179
        ('019d79ef-d9db-78aa-a644-7c872c69b4e4', 3),  -- 217, 1269
        ('019d79ef-d9dc-773f-aad5-28ca2a2188ae', 2),  -- 279, 1269
        ('019d79ef-d9da-7bac-836c-2a18cb7cf3ce', 2),  -- 298, 170
        ('019d79ef-d9db-762d-bd26-131e6e9c02bb', 3),  -- 324, 248
        ('019d79ef-d9da-7014-8048-4b032f2eeb9f', 2),  -- 359, 106
        ('019d79ef-d9dc-7b3f-b990-0ccfb7a46e3b', 1),  -- 422, 1359
        ('019d79ef-d9dc-7fb8-831b-330c6a73c2fa', 3),  -- 634, -5
        ('019d79ef-d9dd-788f-91ae-117a9b6c72c6', 2),  -- 953, 31
        ('019d79ef-d9dd-7707-930c-bf5682cdb66c', 1),  -- 1108, 1393
        ('019d79ef-d9dc-7736-ac06-754f3067e06e', 3),  -- 1494, 1371
        ('019d79ef-d9dc-71d0-84b7-53c64b79e98d', 2),  -- 1517, 1007
        ('019d79ef-d9dc-7b9e-a46b-56c819d0f758', 1),  -- 1527, 1072
        ('019d79ef-d9dc-7b15-b52c-050ebc5334ba', 3),  -- 1592, 1008
        ('019d79ef-d9dd-7fdb-a673-86a8a062de1d', 2),  -- 1680, 1360
        ('019d79ef-d9dd-74e2-a46e-e10803504abe', 1);  -- 1786, 99

    UPDATE village_object o
       SET asset_id = CASE p.variant WHEN 1 THEN maple WHEN 2 THEN maple2 ELSE maple3 END
      FROM llm967v_place p
     WHERE o.id = p.object_id
       AND o.asset_id IN (maple, maple2, maple3);

    -- Every maple has exactly the eight states, with exactly these values.
    SELECT count(*) INTO bad FROM (
        (SELECT v.asset_id, l.state, v.sheet, 0 AS src_x, l.src_y, 80 AS src_w, 112 AS src_h, l.frame_count, l.frame_rate
           FROM llm967v_asset v CROSS JOIN llm967v_look l
         EXCEPT
         SELECT s.asset_id, s.state, s.sheet, s.src_x, s.src_y, s.src_w, s.src_h, s.frame_count, s.frame_rate
           FROM asset_state s JOIN llm967v_asset v ON v.asset_id = s.asset_id)
        UNION ALL
        (SELECT s.asset_id, s.state, s.sheet, s.src_x, s.src_y, s.src_w, s.src_h, s.frame_count, s.frame_rate
           FROM asset_state s JOIN llm967v_asset v ON v.asset_id = s.asset_id
         EXCEPT
         SELECT v.asset_id, l.state, v.sheet, 0, l.src_y, 80, 112, l.frame_count, l.frame_rate
           FROM llm967v_asset v CROSS JOIN llm967v_look l)
    ) diff;
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-967: % maple state rows differ from the authored values', bad;
    END IF;

    -- The new maples carry exactly the Maple Tree's tags, state by state.
    SELECT count(*) INTO bad FROM (
        (SELECT v.asset_id, ms.state, t.tag
           FROM asset_state ms JOIN asset_state_tag t ON t.state_id = ms.id, llm967v_asset v
          WHERE ms.asset_id = maple AND v.asset_id <> maple
         EXCEPT
         SELECT s.asset_id, s.state, t.tag
           FROM asset_state s JOIN asset_state_tag t ON t.state_id = s.id
          WHERE s.asset_id IN (maple2, maple3))
        UNION ALL
        (SELECT s.asset_id, s.state, t.tag
           FROM asset_state s JOIN asset_state_tag t ON t.state_id = s.id
          WHERE s.asset_id IN (maple2, maple3)
         EXCEPT
         SELECT v.asset_id, ms.state, t.tag
           FROM asset_state ms JOIN asset_state_tag t ON t.state_id = ms.id, llm967v_asset v
          WHERE ms.asset_id = maple AND v.asset_id <> maple)
    ) diff;
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-967: % season tags differ between the Maple Tree and its variants', bad;
    END IF;

    -- Each pinned maple that exists is on its maple.
    SELECT count(*) INTO bad
      FROM llm967v_place p JOIN village_object o ON o.id = p.object_id
     WHERE o.asset_id <> CASE p.variant WHEN 1 THEN maple WHEN 2 THEN maple2 ELSE maple3 END;
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-967: % placed maples are not on their variant', bad;
    END IF;

    -- The new maples are the Maple Tree in every copied column (null-safe).
    SELECT count(*) INTO bad
      FROM asset a, asset m
     WHERE m.id = maple AND a.id IN (maple2, maple3)
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
    IF bad > 0 OR (SELECT count(*) FROM asset WHERE id IN (maple2, maple3)) <> 2 THEN
        RAISE EXCEPTION 'LLM-967: % maple variants differ from the Maple Tree''s asset columns', bad;
    END IF;

    -- And rest like it: the same refresh-default rows, both directions.
    SELECT count(*) INTO bad FROM (
        (SELECT v.asset_id, d.attribute, d.amount, d.available_quantity, d.max_quantity, d.refresh_mode,
                d.refresh_period_hours, d.dwell_amount, d.dwell_period_minutes, d.gather_item
           FROM asset_refresh_default d, llm967v_asset v
          WHERE d.asset_id = maple AND v.asset_id <> maple
         EXCEPT
         SELECT asset_id, attribute, amount, available_quantity, max_quantity, refresh_mode,
                refresh_period_hours, dwell_amount, dwell_period_minutes, gather_item
           FROM asset_refresh_default WHERE asset_id IN (maple2, maple3))
        UNION ALL
        (SELECT asset_id, attribute, amount, available_quantity, max_quantity, refresh_mode,
                refresh_period_hours, dwell_amount, dwell_period_minutes, gather_item
           FROM asset_refresh_default WHERE asset_id IN (maple2, maple3)
         EXCEPT
         SELECT v.asset_id, d.attribute, d.amount, d.available_quantity, d.max_quantity, d.refresh_mode,
                d.refresh_period_hours, d.dwell_amount, d.dwell_period_minutes, d.gather_item
           FROM asset_refresh_default d, llm967v_asset v
          WHERE d.asset_id = maple AND v.asset_id <> maple)
    ) diff;
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-967: % rest-default rows differ between the Maple Tree and its variants', bad;
    END IF;
END $$;

COMMIT;
