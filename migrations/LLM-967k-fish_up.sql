-- LLM-967: painted fish. Three schools in place of Mana Seed's five
-- colourways of one school.
--
-- The School of Fish pieces (LLM-600, LLM-601) drew Mana Seed fishing-gear
-- art, whose licence forbids use alongside AI. tools/tree-art/fish.py now
-- paints three schools: client/assets/trees/shiners.png, perch.png and
-- brook-trout.png. This migration sorts after LLM-967j-stumps (salem
-- migrations run in filename order).
--
-- Which piece becomes which: the two placed schools keep their ids and their
-- places. School of Fish (cyan) becomes Shiners and School of Fish (green)
-- becomes Perch. School of Fish (blue), never placed, becomes Brook Trout.
-- School of Fish (brown) and School of Fish (sage), never placed, are retired:
-- three kinds of fish are enough, and the five colourways were one school
-- recoloured. If either has been placed since, this migration fails rather
-- than leave a placement pointing at nothing (village_object has no foreign
-- key to asset).
--
-- Sheet layout (fish.py): one row, a seamless 48-frame swim at 8 fps, each
-- frame 48x48. The frame is larger than the pack's 32x32 so the fish have
-- room to swim; the anchor stays the frame's middle (0.5, 0.5) and
-- render_scale stays 2, so a placed school stays where it is. Every other
-- field of the asset row (category, layer, z_index 1, pack, obstacle) is
-- unchanged; only the name and source_file change.
--
-- For each piece that exists (a fresh database has no catalog; then it does
-- nothing), it gets its name, source_file and its one state, and then every
-- piece must have exactly that, and the retired pieces must be gone, or the
-- migration fails.
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
    -- The three schools: the asset each becomes, and its sheet.
    CREATE TEMP TABLE llm967k_fish (asset_id uuid PRIMARY KEY, name text NOT NULL, sheet text NOT NULL)
        ON COMMIT DROP;
    INSERT INTO llm967k_fish VALUES
        ('019e5f00-c401-7a10-9e00-000000600001', 'Shiners',     'res://assets/trees/shiners.png'),      -- was School of Fish (cyan)
        ('019e5f00-c401-7a10-9e00-000000600002', 'Perch',       'res://assets/trees/perch.png'),        -- was School of Fish (green)
        ('019e5f00-c401-7a10-9e00-000000601001', 'Brook Trout', 'res://assets/trees/brook-trout.png');  -- was School of Fish (blue)
    -- The retired colourways.
    CREATE TEMP TABLE llm967k_retired (asset_id uuid PRIMARY KEY) ON COMMIT DROP;
    INSERT INTO llm967k_retired VALUES
        ('019e5f00-c401-7a10-9e00-000000601002'),   -- School of Fish (brown)
        ('019e5f00-c401-7a10-9e00-000000601003');   -- School of Fish (sage)

    -- A piece that is not in this database takes no part.
    DELETE FROM llm967k_fish v WHERE NOT EXISTS (SELECT 1 FROM asset WHERE id = v.asset_id);

    -- A retired piece must not be placed: retiring it would leave the
    -- placement pointing at nothing.
    SELECT count(*) INTO placed FROM village_object WHERE asset_id IN (SELECT asset_id FROM llm967k_retired);
    IF placed > 0 THEN
        RAISE EXCEPTION 'LLM-967k: % placement(s) of School of Fish (brown) or (sage) in the village; remove them (engine stopped) first', placed;
    END IF;
    -- Their states' tags and lights, and any refresh default, go with them
    -- (ON DELETE CASCADE).
    DELETE FROM asset_state WHERE asset_id IN (SELECT asset_id FROM llm967k_retired);
    DELETE FROM asset WHERE id IN (SELECT asset_id FROM llm967k_retired);

    IF NOT EXISTS (SELECT 1 FROM llm967k_fish) THEN
        RETURN;
    END IF;

    UPDATE asset a
       SET name = v.name, source_file = 'tools/tree-art/fish.py'
      FROM llm967k_fish v
     WHERE a.id = v.asset_id;

    -- Every school's one state: the swim loop, as authored.
    CREATE TEMP TABLE llm967k_state ON COMMIT DROP AS
    SELECT asset_id, 'default'::text AS state, sheet, 0 AS src_x, 0 AS src_y, 48 AS src_w, 48 AS src_h,
           48 AS frame_count, 8::double precision AS frame_rate
      FROM llm967k_fish;

    INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
    SELECT asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate
      FROM llm967k_state
    ON CONFLICT (asset_id, state) DO UPDATE
       SET sheet = EXCLUDED.sheet, src_x = EXCLUDED.src_x, src_y = EXCLUDED.src_y, src_w = EXCLUDED.src_w,
           src_h = EXCLUDED.src_h, frame_count = EXCLUDED.frame_count, frame_rate = EXCLUDED.frame_rate;

    -- Every school has exactly its one state, with exactly these values, its
    -- name and source_file, and its default state is that state.
    SELECT count(*) INTO bad FROM (
        (SELECT asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate FROM llm967k_state
         EXCEPT
         SELECT s.asset_id, s.state, s.sheet, s.src_x, s.src_y, s.src_w, s.src_h, s.frame_count, s.frame_rate
           FROM asset_state s JOIN llm967k_fish v ON v.asset_id = s.asset_id)
        UNION ALL
        (SELECT s.asset_id, s.state, s.sheet, s.src_x, s.src_y, s.src_w, s.src_h, s.frame_count, s.frame_rate
           FROM asset_state s JOIN llm967k_fish v ON v.asset_id = s.asset_id
         EXCEPT
         SELECT asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate FROM llm967k_state)
    ) diff;
    SELECT bad + count(*) INTO bad
      FROM asset a JOIN llm967k_fish v ON v.asset_id = a.id
     WHERE a.name <> v.name OR a.source_file IS DISTINCT FROM 'tools/tree-art/fish.py'
        OR a.default_state <> 'default';
    SELECT bad + count(*) INTO bad FROM asset WHERE id IN (SELECT asset_id FROM llm967k_retired);
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-967k: % fish row(s) differ from the authored values', bad;
    END IF;
END $$;

COMMIT;
