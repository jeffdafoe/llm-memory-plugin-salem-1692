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
-- replace it. If it has been placed since, this migration fails rather than
-- leave a placement pointing at nothing (village_object has no foreign key to
-- asset).
--
-- For each piece that exists (a fresh database has no catalog; then it does
-- nothing), its render_scale becomes 1 and its states point at its sheet in
-- that layout, and then every piece must have exactly those states with
-- exactly those values, or the migration fails.
--
-- ENGINE-OWNED data (the catalog), read at boot; deploy.sh runs stop ->
-- migrate -> start. Convergent: rerunning ends in the same place, and a rerun
-- repairs a hand edit of these rows.

BEGIN;

DO $$
DECLARE
    bad integer;
    placed integer;
BEGIN
    -- Berry Bush Cluster must not be placed: retiring it would leave the
    -- placement pointing at nothing.
    SELECT count(*) INTO placed FROM village_object WHERE asset_id = 'd699b17f-c743-48d4-8bb9-debaba884a55';
    IF placed > 0 THEN
        RAISE EXCEPTION 'LLM-967l: % placement(s) of Berry Bush Cluster in the village; remove them (engine stopped) first', placed;
    END IF;
    -- Its states' tags and lights go with them (ON DELETE CASCADE); it has
    -- no refresh default.
    DELETE FROM asset_state WHERE asset_id = 'd699b17f-c743-48d4-8bb9-debaba884a55';
    DELETE FROM asset WHERE id = 'd699b17f-c743-48d4-8bb9-debaba884a55';

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

    INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
    SELECT asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate
      FROM llm967l_state
    ON CONFLICT (asset_id, state) DO UPDATE
       SET sheet = EXCLUDED.sheet, src_x = EXCLUDED.src_x, src_y = EXCLUDED.src_y, src_w = EXCLUDED.src_w,
           src_h = EXCLUDED.src_h, frame_count = EXCLUDED.frame_count, frame_rate = EXCLUDED.frame_rate;

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
