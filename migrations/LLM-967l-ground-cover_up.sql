-- LLM-967: painted ground cover: the grasses (Tall, Medium, Thick, Low and
-- Light Grass), Fern, Clover, the five mushrooms (Brown, Grey, Yellow, White,
-- Layered), Pine Cone, Leaf, Twig, Small Branch and Seeds (now acorns).
--
-- They drew Mana Seed forest art, whose licence forbids use alongside AI.
-- tools/tree-art/plants.py now paints them: client/assets/trees/<piece>.png
-- (tall-grass, medium-grass, thick-grass, low-grass, light-grass, fern,
-- clover, brown-mushroom, grey-mushroom, yellow-mushroom, white-mushroom,
-- layered-mushroom, pine-cone, leaf, twig, small-branch, seeds). This
-- migration sorts after LLM-967k-fish (salem migrations run in filename
-- order).
--
-- Sheet layout (plants.py): one row per look, top to bottom: summer, autumn,
-- winter, winter-snowy. The grasses and the fern rustle: 36 frames at 6 fps.
-- Every other piece is one still frame. Frames are twice the pack art's size
-- (32x32; Tall, Medium, Thick and Low Grass 32x64) and drawn at render_scale 1
-- instead of 2, so every piece is the same size in the world with twice the
-- detail (as dense as the ground's 32 px tiles; Jeff chose it on 2026-11-25).
-- The anchor stays 0.5/0.85, so every placed piece keeps its place.
--
-- No state is added or removed, and no tag changes: each existing state points
-- at its look's row (default -> summer, autumn -> autumn, winter-clean ->
-- winter, winter-snowy -> winter-snowy). No piece is copied and no placement
-- moves.
--
-- Berry Bush Cluster is retired. LLM-58 moved its 18 placements onto the
-- Blueberry Bush, and the painted blueberry and raspberry bushes (LLM-967g)
-- replace it. It is retired only where the down can bring it back exactly:
-- it is not placed (village_object has no foreign key to asset, so a placement
-- would be left pointing at nothing), the Fern is here (the down's sign of the
-- forest catalog), and it is just what the down recreates (its asset row, its
-- four states with their season tags, no lights). Otherwise the migration
-- fails rather than lose it.
--
-- For each piece that exists (a fresh database has no catalog; then it does
-- nothing), its render_scale becomes 1 and its states point at its sheet in
-- that layout, and then every piece must have exactly those states with
-- exactly those values, or the migration fails. A missing state is not
-- recreated, since it would come back without its season tag; the check fails
-- on it instead.
--
-- ENGINE-OWNED data (the catalog), read at boot; deploy.sh runs stop ->
-- migrate -> start. Convergent: rerunning ends in the same place, and a rerun
-- repairs a hand edit of a state's rect.

BEGIN;

DO $$
DECLARE
    cluster CONSTANT uuid := 'd699b17f-c743-48d4-8bb9-debaba884a55';
    bad integer;
    placed integer;
BEGIN
    IF EXISTS (SELECT 1 FROM asset WHERE id = cluster) THEN
        SELECT count(*) INTO placed FROM village_object WHERE asset_id = cluster;
        IF placed > 0 THEN
            RAISE EXCEPTION 'LLM-967l: % placement(s) of Berry Bush Cluster in the village; remove them (engine stopped) first', placed;
        END IF;
        IF NOT EXISTS (SELECT 1 FROM asset WHERE id = 'b4c226f6-e0af-444f-ba0e-5d4efcd5c8d7') THEN
            RAISE EXCEPTION 'LLM-967l: Berry Bush Cluster is here without the Fern, so the down could not bring it back';
        END IF;
        -- The asset row, column by column, as the down recreates it.
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
            RAISE EXCEPTION 'LLM-967l: Berry Bush Cluster''s asset row differs from the one the down recreates';
        END IF;
        -- Its states, each with its tags and its count of lights, both ways.
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
                ('default',      '/tilesets/mana-seed/summer-forest/summer sheets/summer 16x32.png',          '2026-04-11 18:50:27'::timestamp, ''),
                ('autumn',       '/tilesets/mana-seed/autumn-forest/autumn sheets/autumn 16x32.png',          '2026-10-23 18:59:33'::timestamp, 'season-autumn'),
                ('winter-clean', '/tilesets/mana-seed/winter-forest/winter sheets/winter (clean) 16x32.png', '2026-10-23 18:59:33'::timestamp, 'season-winter-clean'),
                ('winter-snowy', '/tilesets/mana-seed/winter-forest/winter sheets/winter (snowy) 16x32.png', '2026-10-23 18:59:33'::timestamp, 'season-winter-snowy'))
               v(state, sheet, created_at, tags);
        SELECT count(*) INTO bad FROM (
            (SELECT * FROM llm967l_cluster_expected EXCEPT SELECT * FROM llm967l_cluster_actual)
            UNION ALL
            (SELECT * FROM llm967l_cluster_actual EXCEPT SELECT * FROM llm967l_cluster_expected)
        ) diff;
        IF bad > 0 THEN
            RAISE EXCEPTION 'LLM-967l: % of Berry Bush Cluster''s state rows (with tags and lights) differ from the ones the down recreates', bad;
        END IF;
        -- Its states' tags go with them (ON DELETE CASCADE); it has no lights
        -- (checked above) and no refresh default.
        DELETE FROM asset_state WHERE asset_id = cluster;
        DELETE FROM asset WHERE id = cluster;
    END IF;

    -- Every piece: its sheet, frame size and play rate.
    CREATE TEMP TABLE llm967l_asset (asset_id uuid PRIMARY KEY, sheet text NOT NULL, frame_w integer NOT NULL,
                                     frame_h integer NOT NULL, frame_count integer NOT NULL,
                                     frame_rate double precision NOT NULL) ON COMMIT DROP;
    INSERT INTO llm967l_asset VALUES
        ('72380aa3-1331-429b-b298-6a1325de1131', 'res://assets/trees/tall-grass.png',       32, 64, 36, 6),  -- Tall Grass
        ('c060819b-1d00-42ac-9013-6db9e9e395b3', 'res://assets/trees/medium-grass.png',     32, 64, 36, 6),  -- Medium Grass
        ('9ea9d7d9-f0d9-4ba1-8262-3a12642abad4', 'res://assets/trees/thick-grass.png',      32, 64, 36, 6),  -- Thick Grass
        ('aab9acf0-c3f9-42e0-9c1d-d37f8ce4d330', 'res://assets/trees/low-grass.png',        32, 64, 36, 6),  -- Low Grass
        ('d0c57155-0aad-486d-a29c-e6fd466a2885', 'res://assets/trees/light-grass.png',      32, 32, 36, 6),  -- Light Grass
        ('b4c226f6-e0af-444f-ba0e-5d4efcd5c8d7', 'res://assets/trees/fern.png',             32, 32, 36, 6),  -- Fern
        ('30e41455-48f3-46f0-99c6-bb2ae6e95a52', 'res://assets/trees/clover.png',           32, 32,  1, 0),  -- Clover
        ('a9d72130-ec8a-457a-9eeb-f1f3785791d7', 'res://assets/trees/brown-mushroom.png',   32, 32,  1, 0),  -- Brown Mushroom
        ('abcfbe8d-9113-44df-ab51-a07e6fdd6407', 'res://assets/trees/grey-mushroom.png',    32, 32,  1, 0),  -- Grey Mushroom
        ('37a0cacb-0f46-4960-8c38-43b888061ffc', 'res://assets/trees/yellow-mushroom.png',  32, 32,  1, 0),  -- Yellow Mushroom
        ('4fb54538-7b19-4bfe-bd49-d0072784c0a3', 'res://assets/trees/white-mushroom.png',   32, 32,  1, 0),  -- White Mushroom
        ('9e6e19c8-b7dd-4ade-87b1-2965dff5727b', 'res://assets/trees/layered-mushroom.png', 32, 32,  1, 0),  -- Layered Mushroom
        ('a5fa1854-bd44-4d27-88ae-158a93e4ca0a', 'res://assets/trees/pine-cone.png',        32, 32,  1, 0),  -- Pine Cone
        ('d8b77103-3d08-4f98-b293-bbac615dbf38', 'res://assets/trees/leaf.png',             32, 32,  1, 0),  -- Leaf
        ('b0f97cf8-5443-4a93-92c6-2486c803c3b1', 'res://assets/trees/twig.png',             32, 32,  1, 0),  -- Twig
        ('a13dba14-932e-4ae1-8df0-96d7b31133dd', 'res://assets/trees/small-branch.png',     32, 32,  1, 0),  -- Small Branch
        ('bab2e75d-7088-487e-8561-a0a16c37c37a', 'res://assets/trees/seeds.png',            32, 32,  1, 0);  -- Seeds
    -- A piece that is not in this database takes no part.
    DELETE FROM llm967l_asset v WHERE NOT EXISTS (SELECT 1 FROM asset WHERE id = v.asset_id);
    IF NOT EXISTS (SELECT 1 FROM llm967l_asset) THEN
        RETURN;
    END IF;

    UPDATE asset a SET render_scale = 1 FROM llm967l_asset v WHERE a.id = v.asset_id;

    -- Every state of every piece, as authored: the state and the sheet row it shows.
    CREATE TEMP TABLE llm967l_state ON COMMIT DROP AS
    SELECT v.asset_id, l.state, v.sheet, 0 AS src_x, l.row_index * v.frame_h AS src_y, v.frame_w AS src_w,
           v.frame_h AS src_h, v.frame_count, v.frame_rate
      FROM llm967l_asset v
     CROSS JOIN (VALUES ('default', 0), ('autumn', 1), ('winter-clean', 2), ('winter-snowy', 3)) l(state, row_index);

    -- Existing states only: the check below fails on a missing one.
    UPDATE asset_state s
       SET sheet = v.sheet, src_x = v.src_x, src_y = v.src_y, src_w = v.src_w, src_h = v.src_h,
           frame_count = v.frame_count, frame_rate = v.frame_rate
      FROM llm967l_state v
     WHERE s.asset_id = v.asset_id AND s.state = v.state;

    -- Every piece has exactly its states, with exactly these values.
    SELECT count(*) INTO bad FROM (
        (SELECT asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate FROM llm967l_state
         EXCEPT
         SELECT s.asset_id, s.state, s.sheet, s.src_x, s.src_y, s.src_w, s.src_h, s.frame_count, s.frame_rate
           FROM asset_state s JOIN llm967l_asset v ON v.asset_id = s.asset_id)
        UNION ALL
        (SELECT s.asset_id, s.state, s.sheet, s.src_x, s.src_y, s.src_w, s.src_h, s.frame_count, s.frame_rate
           FROM asset_state s JOIN llm967l_asset v ON v.asset_id = s.asset_id
         EXCEPT
         SELECT asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate FROM llm967l_state)
    ) diff;
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-967l: % ground-cover state rows differ from the authored values', bad;
    END IF;
    SELECT count(*) INTO bad FROM asset a JOIN llm967l_asset v ON v.asset_id = a.id WHERE a.render_scale <> 1;
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-967l: % ground-cover piece(s) not at render_scale 1', bad;
    END IF;
END $$;

COMMIT;
