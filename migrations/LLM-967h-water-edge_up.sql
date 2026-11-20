-- LLM-967: painted water edge: reeds, lily pads, the water vine, the sunk barrel.
--
-- Cattails, Water Plant, Lily Pad, Light Lily Pad, Small Lily Pad, Tiny Lily
-- Pad, Water Vine and Sunk Barrel drew Mana Seed art, whose licence forbids
-- use alongside AI. tools/tree-art/water.py now paints them:
-- client/assets/trees/cattails-1..3.png, water-plant-1..3.png (three clumps of
-- each), lily-pad.png, light-lily-pad.png, small-lily-pad.png,
-- tiny-lily-pad.png, water-vine.png, sunk-barrel.png. This migration sorts
-- after LLM-967g-berry-bushes (salem migrations run in filename order).
--
-- Sheet layout (water.py): one row per look, top to bottom: summer, autumn,
-- winter, winter-snowy (the sunk barrel has its one look). The reeds' rows are
-- 36 frames played at 6 fps (they rustle); everything else is one still frame.
-- Frames are 16x16, the Lily Pad's 32x32, as the pack art's were, at the same
-- anchor, so every placed piece keeps its place.
--
-- No state is added or removed, and no tag changes: each existing state points
-- at its look's row. The season states map: default -> summer, autumn ->
-- autumn, winter-clean -> winter, winter-snowy -> winter-snowy; the Lily Pad's
-- winter-leaves shows the winter row and its winter-snowy-top the winter-snowy
-- row (the water does not freeze, so a pad has no leaves at its foot to show).
--
-- WHAT this does, for each piece that exists (a fresh database has no catalog;
-- then it does nothing):
--   1. Its states point at its sheet in that layout.
--   2. Cattails 2 / 3 and Water Plant 2 / 3 (pinned ids) are copies of their
--      clump: every asset column but id and name, every state on the -2 / -3
--      sheet, the same tags and rest default (none today), so a new placement
--      is alike.
--   3. The placed clumps (pinned ids: 12 cattails, 6 water plants) are spread
--      over the three, so no clump's nearest clump of its kind is the same
--      variant. Their current state stays with them.
-- Then every one of those must hold exactly, or the migration fails.
--
-- ENGINE-OWNED data (catalog and village_object), read at boot; deploy.sh runs
-- stop -> migrate -> start. Convergent: rerunning ends in the same place, and a
-- rerun repairs a hand edit of these rows.

BEGIN;

DO $$
DECLARE
    cattails CONSTANT uuid := '50be81a0-81ef-48e8-b078-56bd075ee261';
    water_plant CONSTANT uuid := 'cb4fb077-1199-4c24-b1a8-583dd9568762';
    bad integer;
BEGIN
    -- Every asset: its piece (the base it copies, itself for a base), its
    -- sheet, frame and play rate, and the layout of its sheet's rows.
    CREATE TEMP TABLE llm967h_asset (asset_id uuid PRIMARY KEY, base_id uuid NOT NULL, variant integer NOT NULL,
                                     name text NOT NULL, sheet text NOT NULL, frame_w integer NOT NULL,
                                     frame_h integer NOT NULL, frame_count integer NOT NULL,
                                     frame_rate double precision NOT NULL, layout text NOT NULL) ON COMMIT DROP;
    INSERT INTO llm967h_asset VALUES
        (cattails,                               cattails,    1, 'Cattails',       'res://assets/trees/cattails-1.png',    16, 16, 36, 6, 'seasons'),
        ('86a9b700-74d1-4fe8-9433-15176dee059c', cattails,    2, 'Cattails 2',     'res://assets/trees/cattails-2.png',    16, 16, 36, 6, 'seasons'),
        ('8c9734a5-1137-4753-bfd2-8a9557b49956', cattails,    3, 'Cattails 3',     'res://assets/trees/cattails-3.png',    16, 16, 36, 6, 'seasons'),
        (water_plant,                            water_plant, 1, 'Water Plant',    'res://assets/trees/water-plant-1.png', 16, 16, 36, 6, 'seasons'),
        ('a40926e8-73cc-4040-a64b-f14c161d67cf', water_plant, 2, 'Water Plant 2',  'res://assets/trees/water-plant-2.png', 16, 16, 36, 6, 'seasons'),
        ('02abb1dd-16fc-42f8-8161-7c56cb1353a6', water_plant, 3, 'Water Plant 3',  'res://assets/trees/water-plant-3.png', 16, 16, 36, 6, 'seasons'),
        ('50146fff-7bb4-4805-a65a-1e1fd4c9e97c', '50146fff-7bb4-4805-a65a-1e1fd4c9e97c', 1, 'Lily Pad',
         'res://assets/trees/lily-pad.png',       32, 32, 1, 0, 'seasons-six'),
        ('bf824093-528d-4cec-9d9a-f2ac07f6a1dd', 'bf824093-528d-4cec-9d9a-f2ac07f6a1dd', 1, 'Light Lily Pad',
         'res://assets/trees/light-lily-pad.png', 16, 16, 1, 0, 'seasons'),
        ('55d4951b-0400-4588-a9d9-3274c9138cf6', '55d4951b-0400-4588-a9d9-3274c9138cf6', 1, 'Small Lily Pad',
         'res://assets/trees/small-lily-pad.png', 16, 16, 1, 0, 'seasons'),
        ('e78bd2e8-6932-4312-bff0-25412474f461', 'e78bd2e8-6932-4312-bff0-25412474f461', 1, 'Tiny Lily Pad',
         'res://assets/trees/tiny-lily-pad.png',  16, 16, 1, 0, 'seasons'),
        ('ab4a39b1-7bda-4c7b-8d85-dcf7d16395a6', 'ab4a39b1-7bda-4c7b-8d85-dcf7d16395a6', 1, 'Water Vine',
         'res://assets/trees/water-vine.png',     16, 16, 1, 0, 'seasons'),
        ('5e947427-6c27-4d54-afda-c6fa494470b6', '5e947427-6c27-4d54-afda-c6fa494470b6', 1, 'Sunk Barrel',
         'res://assets/trees/sunk-barrel.png',    16, 16, 1, 0, 'one');
    -- A piece that is not in this database takes no part.
    DELETE FROM llm967h_asset v WHERE NOT EXISTS (SELECT 1 FROM asset WHERE id = v.base_id);
    IF NOT EXISTS (SELECT 1 FROM llm967h_asset) THEN
        RETURN;
    END IF;

    -- Each layout's states and the sheet row each one shows.
    CREATE TEMP TABLE llm967h_look (layout text NOT NULL, state text NOT NULL, row_index integer NOT NULL,
                                    PRIMARY KEY (layout, state)) ON COMMIT DROP;
    INSERT INTO llm967h_look VALUES
        ('seasons',     'default',          0),
        ('seasons',     'autumn',           1),
        ('seasons',     'winter-clean',     2),
        ('seasons',     'winter-snowy',     3),
        ('seasons-six', 'default',          0),
        ('seasons-six', 'autumn',           1),
        ('seasons-six', 'winter-clean',     2),
        ('seasons-six', 'winter-leaves',    2),
        ('seasons-six', 'winter-snowy-top', 3),
        ('seasons-six', 'winter-snowy',     3),
        ('one',         'default',          0);

    -- Every state of every asset, as authored.
    CREATE TEMP TABLE llm967h_state ON COMMIT DROP AS
    SELECT v.asset_id, l.state, v.sheet, 0 AS src_x, l.row_index * v.frame_h AS src_y, v.frame_w AS src_w,
           v.frame_h AS src_h, v.frame_count, v.frame_rate
      FROM llm967h_asset v JOIN llm967h_look l ON l.layout = v.layout;

    -- 2. The new assets: their clump's columns, their own id and name.
    INSERT INTO asset (id, name, category, default_state, anchor_x, anchor_y, layer, pack_id, interior, fits_slot,
                       source_file, rotation_algo, transition_spread_seconds, is_obstacle, is_passage, z_index,
                       footprint_left, footprint_right, footprint_top, footprint_bottom, door_offset_x, door_offset_y,
                       visible_when_inside, stand_offset_x, stand_offset_y, occupied_min_count, occupied_night_only, render_scale)
    SELECT v.asset_id, v.name, m.category, m.default_state, m.anchor_x, m.anchor_y, m.layer, m.pack_id, m.interior, m.fits_slot,
           m.source_file, m.rotation_algo, m.transition_spread_seconds, m.is_obstacle, m.is_passage, m.z_index,
           m.footprint_left, m.footprint_right, m.footprint_top, m.footprint_bottom, m.door_offset_x, m.door_offset_y,
           m.visible_when_inside, m.stand_offset_x, m.stand_offset_y, m.occupied_min_count, m.occupied_night_only, m.render_scale
      FROM llm967h_asset v JOIN asset m ON m.id = v.base_id
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

    -- 1 and 2. Every state of every piece on its sheet, in its row.
    INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
    SELECT asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate
      FROM llm967h_state
    ON CONFLICT (asset_id, state) DO UPDATE
       SET sheet = EXCLUDED.sheet, src_x = EXCLUDED.src_x, src_y = EXCLUDED.src_y, src_w = EXCLUDED.src_w,
           src_h = EXCLUDED.src_h, frame_count = EXCLUDED.frame_count, frame_rate = EXCLUDED.frame_rate;

    -- 2. The copies carry exactly their clump's tags, state by state: cleared
    -- and copied fresh, so a rerun repairs a hand edit.
    DELETE FROM asset_state_tag
     WHERE state_id IN (SELECT s.id FROM asset_state s
                          JOIN llm967h_asset v ON v.asset_id = s.asset_id AND v.asset_id <> v.base_id);
    INSERT INTO asset_state_tag (state_id, tag)
    SELECT vs.id, t.tag
      FROM llm967h_asset v
      JOIN asset_state ms ON ms.asset_id = v.base_id
      JOIN asset_state_tag t ON t.state_id = ms.id
      JOIN asset_state vs ON vs.asset_id = v.asset_id AND vs.state = ms.state
     WHERE v.asset_id <> v.base_id;

    -- And its rest default (none today), replaced by a fresh copy.
    DELETE FROM asset_refresh_default
     WHERE asset_id IN (SELECT asset_id FROM llm967h_asset WHERE asset_id <> base_id);
    INSERT INTO asset_refresh_default (asset_id, attribute, amount, available_quantity, max_quantity, refresh_mode,
                                       refresh_period_hours, dwell_amount, dwell_period_minutes, gather_item)
    SELECT v.asset_id, d.attribute, d.amount, d.available_quantity, d.max_quantity, d.refresh_mode,
           d.refresh_period_hours, d.dwell_amount, d.dwell_period_minutes, d.gather_item
      FROM llm967h_asset v JOIN asset_refresh_default d ON d.asset_id = v.base_id
     WHERE v.asset_id <> v.base_id;

    -- 3. The placed clumps, each to its variant, west to east.
    CREATE TEMP TABLE llm967h_place (object_id uuid PRIMARY KEY, base_id uuid NOT NULL, variant integer NOT NULL) ON COMMIT DROP;
    INSERT INTO llm967h_place VALUES
        ('019da6cb-3f38-7d62-8139-1e18764f42ae', cattails, 1),     -- -529, -2954
        ('019dcacb-e05b-7acf-95b2-cababdace3c5', cattails, 1),     -- -76, -1238
        ('019dcacc-d635-7d2a-90f5-99ad55f12704', cattails, 3),     -- -69, -1228
        ('019dcacd-1652-7f0f-bdbe-becd4f2bb108', cattails, 2),     -- -67, -1215
        ('019dcacb-c22c-7ab4-8c1f-e8de4c1ac645', cattails, 3),     -- -66, -1255
        ('019dcacc-f73f-7e55-af3b-fa16cf1cd2a0', cattails, 2),     -- -46, -1268
        ('019d98ca-b2f1-7f4f-baab-90a8df084b97', cattails, 2),     -- 3734, 242
        ('019d98ca-73a1-7219-b749-f611e46e1490', cattails, 1),     -- 3739, 226
        ('019d98ca-1689-7dba-94f7-e04ac69e6461', cattails, 3),     -- 3752, 231
        ('019d98ca-f484-7d71-af10-c2f4908a3254', cattails, 2),     -- 3788, 379
        ('019d98ca-e194-7169-8838-c4f27343454a', cattails, 1),     -- 3801, 394
        ('019d98ca-cab5-75db-9707-1f29b5926742', cattails, 3),     -- 3812, 407
        ('019da6cb-5fab-72d0-b50e-14761a1ddc09', water_plant, 2),  -- -569, -2957
        ('019da6cb-1bd4-79c1-8192-5f6d7feae806', water_plant, 3),  -- -548, -2957
        ('019dcacd-3e74-736c-b66b-2980ab729560', water_plant, 1),  -- -81, -1205
        ('019dcacd-618b-70e5-bffa-c42d93a3a244', water_plant, 2),  -- -55, -1255
        ('019d98bf-5805-7703-9156-bed6075b34bf', water_plant, 3),  -- 4034, 194
        ('019d98bf-7d9f-7d0b-a747-433dbe1308ca', water_plant, 1);  -- 4037, 210

    -- Only a clump still on one of its own kind's assets moves (a placed id
    -- hand-switched to some other asset is left alone).
    UPDATE village_object o
       SET asset_id = v.asset_id
      FROM llm967h_place p
      JOIN llm967h_asset v ON v.base_id = p.base_id AND v.variant = p.variant
     WHERE o.id = p.object_id
       AND o.asset_id IN (SELECT asset_id FROM llm967h_asset WHERE base_id = p.base_id);

    -- Every piece has exactly its states, with exactly these values.
    SELECT count(*) INTO bad FROM (
        (SELECT asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate FROM llm967h_state
         EXCEPT
         SELECT s.asset_id, s.state, s.sheet, s.src_x, s.src_y, s.src_w, s.src_h, s.frame_count, s.frame_rate
           FROM asset_state s JOIN llm967h_asset v ON v.asset_id = s.asset_id)
        UNION ALL
        (SELECT s.asset_id, s.state, s.sheet, s.src_x, s.src_y, s.src_w, s.src_h, s.frame_count, s.frame_rate
           FROM asset_state s JOIN llm967h_asset v ON v.asset_id = s.asset_id
         EXCEPT
         SELECT asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate FROM llm967h_state)
    ) diff;
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-967h: % water-edge state rows differ from the authored values', bad;
    END IF;

    -- The copies carry exactly their clump's tags, state by state.
    SELECT count(*) INTO bad FROM (
        (SELECT v.asset_id, ms.state, t.tag
           FROM llm967h_asset v
           JOIN asset_state ms ON ms.asset_id = v.base_id
           JOIN asset_state_tag t ON t.state_id = ms.id
          WHERE v.asset_id <> v.base_id
         EXCEPT
         SELECT s.asset_id, s.state, t.tag
           FROM asset_state s
           JOIN asset_state_tag t ON t.state_id = s.id
           JOIN llm967h_asset v ON v.asset_id = s.asset_id AND v.asset_id <> v.base_id)
        UNION ALL
        (SELECT s.asset_id, s.state, t.tag
           FROM asset_state s
           JOIN asset_state_tag t ON t.state_id = s.id
           JOIN llm967h_asset v ON v.asset_id = s.asset_id AND v.asset_id <> v.base_id
         EXCEPT
         SELECT v.asset_id, ms.state, t.tag
           FROM llm967h_asset v
           JOIN asset_state ms ON ms.asset_id = v.base_id
           JOIN asset_state_tag t ON t.state_id = ms.id
          WHERE v.asset_id <> v.base_id)
    ) diff;
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-967h: % tags differ between a clump and its copies', bad;
    END IF;

    -- Each pinned clump that exists, and is still on its own kind's assets, is
    -- on its variant.
    SELECT count(*) INTO bad
      FROM llm967h_place p
      JOIN village_object o ON o.id = p.object_id
      JOIN llm967h_asset v ON v.base_id = p.base_id AND v.variant = p.variant
     WHERE o.asset_id <> v.asset_id
       AND o.asset_id IN (SELECT asset_id FROM llm967h_asset WHERE base_id = p.base_id);
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-967h: % placed clumps are not on their variant', bad;
    END IF;

    -- The copies are their clump in every copied column (null-safe).
    SELECT count(*) INTO bad
      FROM llm967h_asset v
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
    IF bad > 0 OR (SELECT count(*) FROM asset a JOIN llm967h_asset v ON v.asset_id = a.id)
                  <> (SELECT count(*) FROM llm967h_asset) THEN
        RAISE EXCEPTION 'LLM-967h: % clump copies differ from their clump''s asset columns', bad;
    END IF;

    -- And the same rest-default rows, both directions.
    SELECT count(*) INTO bad FROM (
        (SELECT v.asset_id, d.attribute, d.amount, d.available_quantity, d.max_quantity, d.refresh_mode,
                d.refresh_period_hours, d.dwell_amount, d.dwell_period_minutes, d.gather_item
           FROM llm967h_asset v JOIN asset_refresh_default d ON d.asset_id = v.base_id
          WHERE v.asset_id <> v.base_id
         EXCEPT
         SELECT d.asset_id, d.attribute, d.amount, d.available_quantity, d.max_quantity, d.refresh_mode,
                d.refresh_period_hours, d.dwell_amount, d.dwell_period_minutes, d.gather_item
           FROM asset_refresh_default d JOIN llm967h_asset v ON v.asset_id = d.asset_id AND v.asset_id <> v.base_id)
        UNION ALL
        (SELECT d.asset_id, d.attribute, d.amount, d.available_quantity, d.max_quantity, d.refresh_mode,
                d.refresh_period_hours, d.dwell_amount, d.dwell_period_minutes, d.gather_item
           FROM asset_refresh_default d JOIN llm967h_asset v ON v.asset_id = d.asset_id AND v.asset_id <> v.base_id
         EXCEPT
         SELECT v.asset_id, d.attribute, d.amount, d.available_quantity, d.max_quantity, d.refresh_mode,
                d.refresh_period_hours, d.dwell_amount, d.dwell_period_minutes, d.gather_item
           FROM llm967h_asset v JOIN asset_refresh_default d ON d.asset_id = v.base_id
          WHERE v.asset_id <> v.base_id)
    ) diff;
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-967h: % rest-default rows differ between a clump and its copies', bad;
    END IF;
END $$;

COMMIT;
