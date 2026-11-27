-- LLM-968 down: the Wood Sign Post and the Sign Board back on the Mana Seed
-- village-accessories sheets at render_scale 2, as the pack catalog set them.
-- Tags are unchanged, and checked.
--
-- Each asset that exists is set, then checked both ways; any difference fails
-- the migration. Convergent.

BEGIN;

-- Hold off any other catalog writer (the editor) between the presence check
-- and the checks below.
LOCK TABLE asset, asset_state, asset_state_tag IN SHARE ROW EXCLUSIVE MODE;

DO $$
DECLARE
    bad integer;
BEGIN
    CREATE TEMP TABLE llm968n_down_sign (asset_id uuid PRIMARY KEY) ON COMMIT DROP;
    INSERT INTO llm968n_down_sign VALUES
        ('796d76b4-9a3b-4541-9852-98d389c1906a'),   -- Wood Sign Post
        ('a1f9f3e6-d790-48f9-a169-4c26186e113c');   -- Sign Board
    DELETE FROM llm968n_down_sign v WHERE NOT EXISTS (SELECT 1 FROM asset WHERE id = v.asset_id);
    IF NOT EXISTS (SELECT 1 FROM llm968n_down_sign) THEN
        RETURN;
    END IF;

    -- Every state, as the pack catalog had it.
    CREATE TEMP TABLE llm968n_down_state (asset_id uuid, state text, sheet text, src_x integer, src_y integer,
                                          src_w integer, src_h integer, frame_count integer,
                                          frame_rate double precision) ON COMMIT DROP;
    INSERT INTO llm968n_down_state VALUES
        ('796d76b4-9a3b-4541-9852-98d389c1906a', 'default', '/tilesets/mana-seed/village-accessories/village signposts 48x64.png', 0, 0, 48, 64, 1, 0),
        ('796d76b4-9a3b-4541-9852-98d389c1906a', 'crooked', '/tilesets/mana-seed/village-accessories/village signposts 48x64.png', 96, 0, 48, 64, 1, 0),
        ('a1f9f3e6-d790-48f9-a169-4c26186e113c', 'default', '/tilesets/mana-seed/village-accessories/village accessories 16x16.png', 16, 32, 16, 16, 1, 0);
    DELETE FROM llm968n_down_state v WHERE NOT EXISTS (SELECT 1 FROM llm968n_down_sign l WHERE l.asset_id = v.asset_id);

    CREATE TEMP TABLE llm968n_down_tag (asset_id uuid, state text, tag text) ON COMMIT DROP;
    INSERT INTO llm968n_down_tag
    SELECT a, s, t FROM (VALUES
        ('796d76b4-9a3b-4541-9852-98d389c1906a'::uuid, 'crooked', 'minor-form-signpost'),
        ('796d76b4-9a3b-4541-9852-98d389c1906a'::uuid, 'crooked', 'minor-of-default')) v(a, s, t)
     WHERE EXISTS (SELECT 1 FROM llm968n_down_sign l WHERE l.asset_id = v.a);

    UPDATE asset a SET render_scale = 2 FROM llm968n_down_sign v WHERE a.id = v.asset_id;
    -- Existing states only: the check below fails on a missing one.
    UPDATE asset_state s
       SET sheet = v.sheet, src_x = v.src_x, src_y = v.src_y, src_w = v.src_w, src_h = v.src_h,
           frame_count = v.frame_count, frame_rate = v.frame_rate
      FROM llm968n_down_state v
     WHERE s.asset_id = v.asset_id AND s.state = v.state;

    SELECT count(*) INTO bad FROM (
        (SELECT asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate FROM llm968n_down_state
         EXCEPT
         SELECT s.asset_id, s.state, s.sheet, s.src_x, s.src_y, s.src_w, s.src_h, s.frame_count, s.frame_rate
           FROM asset_state s JOIN llm968n_down_sign v ON v.asset_id = s.asset_id)
        UNION ALL
        (SELECT s.asset_id, s.state, s.sheet, s.src_x, s.src_y, s.src_w, s.src_h, s.frame_count, s.frame_rate
           FROM asset_state s JOIN llm968n_down_sign v ON v.asset_id = s.asset_id
         EXCEPT
         SELECT asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate FROM llm968n_down_state)
    ) diff;
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968n down: % sign state rows differ from the pack values', bad;
    END IF;
    SELECT count(*) INTO bad FROM (
        (SELECT asset_id, state, tag FROM llm968n_down_tag
         EXCEPT
         SELECT s.asset_id, s.state::text, t.tag::text
           FROM asset_state s JOIN llm968n_down_sign v ON v.asset_id = s.asset_id JOIN asset_state_tag t ON t.state_id = s.id)
        UNION ALL
        (SELECT s.asset_id, s.state::text, t.tag::text
           FROM asset_state s JOIN llm968n_down_sign v ON v.asset_id = s.asset_id JOIN asset_state_tag t ON t.state_id = s.id
         EXCEPT
         SELECT asset_id, state, tag FROM llm968n_down_tag)
    ) diff;
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968n down: % sign state tags differ from the expected ones', bad;
    END IF;
    SELECT count(*) INTO bad FROM asset a JOIN llm968n_down_sign v ON v.asset_id = a.id WHERE a.render_scale <> 2;
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968n down: % sign(s) not back at render_scale 2', bad;
    END IF;
END $$;

COMMIT;
