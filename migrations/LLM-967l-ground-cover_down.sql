-- LLM-967l down: the Mana Seed ground cover again. Every piece's states return
-- to its Mana Seed forest rect, one still frame each, at render_scale 2. No
-- state was added or removed by the up, so none is removed here, and no tag
-- or placement changed. Berry Bush Cluster, retired by the up, comes back as
-- it was (its state rows get new ids; nothing pointed at the old ones but
-- their season tags, which come back with them), and the down fails if a
-- cluster already here differs from that.

BEGIN;

DO $$
DECLARE
    summer16 CONSTANT text := '/tilesets/mana-seed/summer-forest/summer sheets/summer 16x16.png';
    autumn16 CONSTANT text := '/tilesets/mana-seed/autumn-forest/autumn sheets/autumn 16x16.png';
    clean16 CONSTANT text := '/tilesets/mana-seed/winter-forest/winter sheets/winter (clean) 16x16.png';
    snowy16 CONSTANT text := '/tilesets/mana-seed/winter-forest/winter sheets/winter (snowy) 16x16.png';
    summer32 CONSTANT text := '/tilesets/mana-seed/summer-forest/summer sheets/summer 16x32.png';
    autumn32 CONSTANT text := '/tilesets/mana-seed/autumn-forest/autumn sheets/autumn 16x32.png';
    clean32 CONSTANT text := '/tilesets/mana-seed/winter-forest/winter sheets/winter (clean) 16x32.png';
    snowy32 CONSTANT text := '/tilesets/mana-seed/winter-forest/winter sheets/winter (snowy) 16x32.png';
    cluster CONSTANT uuid := 'd699b17f-c743-48d4-8bb9-debaba884a55';
    bad integer;
BEGIN
    -- Berry Bush Cluster comes back only where the Mana Seed forest catalog
    -- is here (the Fern, kept by the up, stands for it). A database without
    -- the catalog takes no part.
    IF EXISTS (SELECT 1 FROM asset WHERE id = 'b4c226f6-e0af-444f-ba0e-5d4efcd5c8d7') THEN
        INSERT INTO asset (id, name, category, default_state, anchor_x, anchor_y, layer, created_at, pack_id,
                           interior, fits_slot, source_file, rotation_algo, transition_spread_seconds, is_obstacle,
                           is_passage, z_index, footprint_left, footprint_right, footprint_top, footprint_bottom,
                           door_offset_x, door_offset_y, visible_when_inside, stand_offset_x, stand_offset_y,
                           occupied_min_count, occupied_night_only, render_scale)
        VALUES (cluster, 'Berry Bush Cluster', 'bush', 'default', 0.5, 0.85, 'objects', '2026-04-11 18:50:27',
                'mana-seed', false, NULL, NULL, 'random_per_object', 0, false, false, 10, 0, 0, 0, 0, NULL, NULL,
                false, NULL, NULL, 1, false, 2)
        ON CONFLICT (id) DO NOTHING;
        INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate, created_at)
        VALUES (cluster, 'default',      summer32, 64, 0, 16, 32, 1, 0, '2026-04-11 18:50:27'),
               (cluster, 'autumn',       autumn32, 64, 0, 16, 32, 1, 0, '2026-10-23 18:59:33'),
               (cluster, 'winter-clean', clean32,  64, 0, 16, 32, 1, 0, '2026-10-23 18:59:33'),
               (cluster, 'winter-snowy', snowy32,  64, 0, 16, 32, 1, 0, '2026-10-23 18:59:33')
        ON CONFLICT (asset_id, state) DO NOTHING;
        INSERT INTO asset_state_tag (state_id, tag)
        SELECT s.id, t.tag
          FROM asset_state s
          JOIN (VALUES ('autumn', 'season-autumn'), ('winter-clean', 'season-winter-clean'),
                       ('winter-snowy', 'season-winter-snowy')) t(state, tag) ON t.state = s.state
         WHERE s.asset_id = cluster
        ON CONFLICT DO NOTHING;

        -- The ON CONFLICT clauses make a rerun a no-op, but they would also
        -- keep a cluster row someone changed; so check it is exactly as
        -- recreated: the asset row column by column, and its states with
        -- their tags and lights, both ways.
        IF NOT EXISTS (
            SELECT 1 FROM asset
             WHERE id = cluster AND name = 'Berry Bush Cluster' AND category = 'bush' AND default_state = 'default'
               AND anchor_x = 0.5 AND anchor_y = 0.85 AND layer = 'objects' AND created_at = '2026-04-11 18:50:27'
               AND pack_id IS NOT DISTINCT FROM 'mana-seed' AND interior = false AND fits_slot IS NULL
               AND source_file IS NULL AND rotation_algo = 'random_per_object' AND transition_spread_seconds = 0
               AND is_obstacle = false AND is_passage = false AND z_index = 10 AND footprint_left = 0
               AND footprint_right = 0 AND footprint_top = 0 AND footprint_bottom = 0 AND door_offset_x IS NULL
               AND door_offset_y IS NULL AND visible_when_inside = false AND stand_offset_x IS NULL
               AND stand_offset_y IS NULL AND occupied_min_count = 1 AND occupied_night_only = false
               AND render_scale = 2) THEN
            RAISE EXCEPTION 'LLM-967l down: Berry Bush Cluster''s asset row differs from the one recreated';
        END IF;
        CREATE TEMP TABLE llm967l_cluster_actual ON COMMIT DROP AS
        SELECT s.state::text AS state, s.sheet::text AS sheet, s.src_x, s.src_y, s.src_w, s.src_h, s.frame_count,
               s.frame_rate, s.created_at,
               coalesce((SELECT string_agg(t.tag, ',' ORDER BY t.tag) FROM asset_state_tag t WHERE t.state_id = s.id), '') AS tags,
               (SELECT count(*) FROM asset_state_light l WHERE l.state_id = s.id) AS lights
          FROM asset_state s WHERE s.asset_id = cluster;
        CREATE TEMP TABLE llm967l_cluster_expected ON COMMIT DROP AS
        SELECT v.state, v.sheet, 64 AS src_x, 0 AS src_y, 16 AS src_w, 32 AS src_h, 1 AS frame_count,
               0::double precision AS frame_rate, v.created_at, v.tags, 0::bigint AS lights
          FROM (VALUES
                ('default',      summer32, '2026-04-11 18:50:27'::timestamp, ''),
                ('autumn',       autumn32, '2026-10-23 18:59:33'::timestamp, 'season-autumn'),
                ('winter-clean', clean32,  '2026-10-23 18:59:33'::timestamp, 'season-winter-clean'),
                ('winter-snowy', snowy32,  '2026-10-23 18:59:33'::timestamp, 'season-winter-snowy'))
               v(state, sheet, created_at, tags);
        SELECT count(*) INTO bad FROM (
            (SELECT * FROM llm967l_cluster_expected EXCEPT SELECT * FROM llm967l_cluster_actual)
            UNION ALL
            (SELECT * FROM llm967l_cluster_actual EXCEPT SELECT * FROM llm967l_cluster_expected)
        ) diff;
        IF bad > 0 THEN
            RAISE EXCEPTION 'LLM-967l down: % of Berry Bush Cluster''s state rows (with tags and lights) differ from the ones recreated', bad;
        END IF;
    END IF;

    -- The Mana Seed rects, as they were before LLM-967l: every piece one still
    -- frame per state, the season's sheet for each state.
    CREATE TEMP TABLE llm967l_down (asset_id uuid NOT NULL, state text NOT NULL, sheet text NOT NULL,
                                    src_x integer NOT NULL, src_y integer NOT NULL, src_h integer NOT NULL,
                                    PRIMARY KEY (asset_id, state)) ON COMMIT DROP;
    INSERT INTO llm967l_down
    SELECT p.asset_id::uuid, s.state, s.sheet, p.src_x, p.src_y, 16
      FROM (VALUES ('abcfbe8d-9113-44df-ab51-a07e6fdd6407',  0,  0),    -- Grey Mushroom
                   ('37a0cacb-0f46-4960-8c38-43b888061ffc', 16,  0),    -- Yellow Mushroom
                   ('a9d72130-ec8a-457a-9eeb-f1f3785791d7', 32,  0),    -- Brown Mushroom
                   ('4fb54538-7b19-4bfe-bd49-d0072784c0a3', 48,  0),    -- White Mushroom
                   ('9e6e19c8-b7dd-4ade-87b1-2965dff5727b', 64,  0),    -- Layered Mushroom
                   ('a5fa1854-bd44-4d27-88ae-158a93e4ca0a', 80,  0),    -- Pine Cone
                   ('30e41455-48f3-46f0-99c6-bb2ae6e95a52',  0, 16),    -- Clover
                   ('b4c226f6-e0af-444f-ba0e-5d4efcd5c8d7', 16, 16),    -- Fern
                   ('d8b77103-3d08-4f98-b293-bbac615dbf38', 32, 16),    -- Leaf
                   ('b0f97cf8-5443-4a93-92c6-2486c803c3b1', 48, 16),    -- Twig
                   ('a13dba14-932e-4ae1-8df0-96d7b31133dd', 64, 16),    -- Small Branch
                   ('bab2e75d-7088-487e-8561-a0a16c37c37a', 80, 16),    -- Seeds
                   ('d0c57155-0aad-486d-a29c-e6fd466a2885', 16, 64))    -- Light Grass
           p(asset_id, src_x, src_y)
     CROSS JOIN (VALUES ('default', summer16), ('autumn', autumn16), ('winter-clean', clean16),
                        ('winter-snowy', snowy16)) s(state, sheet);
    INSERT INTO llm967l_down
    SELECT p.asset_id::uuid, s.state, s.sheet, p.src_x, 0, 32
      FROM (VALUES ('aab9acf0-c3f9-42e0-9c1d-d37f8ce4d330',  0),    -- Low Grass
                   ('72380aa3-1331-429b-b298-6a1325de1131', 16),    -- Tall Grass
                   ('c060819b-1d00-42ac-9013-6db9e9e395b3', 32),    -- Medium Grass
                   ('9ea9d7d9-f0d9-4ba1-8262-3a12642abad4', 48))    -- Thick Grass
           p(asset_id, src_x)
     CROSS JOIN (VALUES ('default', summer32), ('autumn', autumn32), ('winter-clean', clean32),
                        ('winter-snowy', snowy32)) s(state, sheet);
    -- A piece that is not in this database takes no part (a fresh database
    -- has no catalog; then this does nothing).
    DELETE FROM llm967l_down d WHERE NOT EXISTS (SELECT 1 FROM asset WHERE id = d.asset_id);

    UPDATE asset_state s
       SET sheet = d.sheet, src_x = d.src_x, src_y = d.src_y, src_w = 16, src_h = d.src_h,
           frame_count = 1, frame_rate = 0
      FROM llm967l_down d
     WHERE s.asset_id = d.asset_id AND s.state = d.state;
    UPDATE asset SET render_scale = 2 WHERE id IN (SELECT asset_id FROM llm967l_down);

    -- Every piece that exists has exactly its Mana Seed states again: no state
    -- missing, none extra, every value as before LLM-967l, at render_scale 2.
    SELECT count(*) INTO bad FROM (
        (SELECT asset_id, state, sheet, src_x, src_y, 16 AS src_w, src_h, 1 AS frame_count,
                0::double precision AS frame_rate
           FROM llm967l_down
         EXCEPT
         SELECT s.asset_id, s.state, s.sheet, s.src_x, s.src_y, s.src_w, s.src_h, s.frame_count, s.frame_rate
           FROM asset_state s WHERE s.asset_id IN (SELECT asset_id FROM llm967l_down))
        UNION ALL
        (SELECT s.asset_id, s.state, s.sheet, s.src_x, s.src_y, s.src_w, s.src_h, s.frame_count, s.frame_rate
           FROM asset_state s WHERE s.asset_id IN (SELECT asset_id FROM llm967l_down)
         EXCEPT
         SELECT asset_id, state, sheet, src_x, src_y, 16, src_h, 1, 0::double precision
           FROM llm967l_down)
    ) diff;
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-967l down: % ground-cover state rows differ from the Mana Seed rects', bad;
    END IF;
    SELECT count(*) INTO bad FROM asset WHERE id IN (SELECT asset_id FROM llm967l_down) AND render_scale <> 2;
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-967l down: % ground-cover piece(s) not back at render_scale 2', bad;
    END IF;
END $$;

COMMIT;
