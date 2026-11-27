-- LLM-968: painted signs: the Wood Sign Post (straight and crooked) and the
-- Sign Board.
--
-- They drew Mana Seed village-accessories art, whose licence forbids use
-- alongside AI. tools/tree-art/signs.py now paints them, at twice the pack
-- art's size and shown at render_scale 1 (the same size in the world, twice
-- the detail). Jeff approved the look on 2026-11-27, after two passes of
-- designer's review.
--
--   client/assets/trees/sign-post.png: one row of two 96x128 frames, the
--     fingerpost straight (default) then crooked (the arm dropped on its
--     lower peg: the minor-damage state).
--   client/assets/trees/sign-board.png: one 32x32 frame.
--
-- Anchors do not change (0.5/0.85; the art's feet are drawn there), so both
-- placements keep their place. No state, tag or placement changes: the
-- crooked state keeps its minor-damage tags (minor-form-signpost,
-- minor-of-default), which damage_minor.go finds it by.
--
-- Each asset that exists is set, then its states and tags are checked both
-- ways against these values, and it must have no lights or slots and keep its
-- anchor; any difference fails the migration. A missing state is not
-- recreated (it would come back without its tags); the check fails on it.
-- An asset not in this database takes no part (no migration creates either).
--
-- ENGINE-OWNED data (the catalog), read at boot; deploy.sh runs stop ->
-- migrate -> start. Convergent: a rerun ends in the same place and repairs a
-- hand edit of a rect.

BEGIN;

-- Hold off any other catalog writer (the editor) between the presence check
-- and the checks below.
LOCK TABLE asset, asset_state, asset_state_tag, asset_state_light, asset_slot IN SHARE ROW EXCLUSIVE MODE;

DO $$
DECLARE
    bad integer;
BEGIN
    CREATE TEMP TABLE llm968n_sign (asset_id uuid PRIMARY KEY) ON COMMIT DROP;
    INSERT INTO llm968n_sign VALUES
        ('796d76b4-9a3b-4541-9852-98d389c1906a'),   -- Wood Sign Post
        ('a1f9f3e6-d790-48f9-a169-4c26186e113c');   -- Sign Board
    DELETE FROM llm968n_sign v WHERE NOT EXISTS (SELECT 1 FROM asset WHERE id = v.asset_id);
    IF NOT EXISTS (SELECT 1 FROM llm968n_sign) THEN
        RETURN;
    END IF;

    -- Every state, as authored.
    CREATE TEMP TABLE llm968n_state (asset_id uuid, state text, sheet text, src_x integer, src_y integer,
                                     src_w integer, src_h integer, frame_count integer,
                                     frame_rate double precision) ON COMMIT DROP;
    INSERT INTO llm968n_state VALUES
        ('796d76b4-9a3b-4541-9852-98d389c1906a', 'default', 'res://assets/trees/sign-post.png', 0, 0, 96, 128, 1, 0),
        ('796d76b4-9a3b-4541-9852-98d389c1906a', 'crooked', 'res://assets/trees/sign-post.png', 96, 0, 96, 128, 1, 0),
        ('a1f9f3e6-d790-48f9-a169-4c26186e113c', 'default', 'res://assets/trees/sign-board.png', 0, 0, 32, 32, 1, 0);
    DELETE FROM llm968n_state v WHERE NOT EXISTS (SELECT 1 FROM llm968n_sign l WHERE l.asset_id = v.asset_id);

    -- Their tags, unchanged by this migration.
    CREATE TEMP TABLE llm968n_tag (asset_id uuid, state text, tag text) ON COMMIT DROP;
    INSERT INTO llm968n_tag
    SELECT a, s, t FROM (VALUES
        ('796d76b4-9a3b-4541-9852-98d389c1906a'::uuid, 'crooked', 'minor-form-signpost'),
        ('796d76b4-9a3b-4541-9852-98d389c1906a'::uuid, 'crooked', 'minor-of-default')) v(a, s, t)
     WHERE EXISTS (SELECT 1 FROM llm968n_sign l WHERE l.asset_id = v.a);

    UPDATE asset a SET render_scale = 1 FROM llm968n_sign v WHERE a.id = v.asset_id;
    -- Existing states only: the check below fails on a missing one.
    UPDATE asset_state s
       SET sheet = v.sheet, src_x = v.src_x, src_y = v.src_y, src_w = v.src_w, src_h = v.src_h,
           frame_count = v.frame_count, frame_rate = v.frame_rate
      FROM llm968n_state v
     WHERE s.asset_id = v.asset_id AND s.state = v.state;

    SELECT count(*) INTO bad FROM (
        (SELECT asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate FROM llm968n_state
         EXCEPT
         SELECT s.asset_id, s.state, s.sheet, s.src_x, s.src_y, s.src_w, s.src_h, s.frame_count, s.frame_rate
           FROM asset_state s JOIN llm968n_sign v ON v.asset_id = s.asset_id)
        UNION ALL
        (SELECT s.asset_id, s.state, s.sheet, s.src_x, s.src_y, s.src_w, s.src_h, s.frame_count, s.frame_rate
           FROM asset_state s JOIN llm968n_sign v ON v.asset_id = s.asset_id
         EXCEPT
         SELECT asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate FROM llm968n_state)
    ) diff;
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968n: % sign state rows differ from the authored values', bad;
    END IF;
    SELECT count(*) INTO bad FROM (
        (SELECT asset_id, state, tag FROM llm968n_tag
         EXCEPT
         SELECT s.asset_id, s.state::text, t.tag::text
           FROM asset_state s JOIN llm968n_sign v ON v.asset_id = s.asset_id JOIN asset_state_tag t ON t.state_id = s.id)
        UNION ALL
        (SELECT s.asset_id, s.state::text, t.tag::text
           FROM asset_state s JOIN llm968n_sign v ON v.asset_id = s.asset_id JOIN asset_state_tag t ON t.state_id = s.id
         EXCEPT
         SELECT asset_id, state, tag FROM llm968n_tag)
    ) diff;
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968n: % sign state tags differ from the expected ones', bad;
    END IF;
    SELECT count(*) INTO bad
      FROM asset_state s JOIN llm968n_sign v ON v.asset_id = s.asset_id JOIN asset_state_light l ON l.state_id = s.id;
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968n: % light(s) on a sign state', bad;
    END IF;
    SELECT count(*) INTO bad FROM asset_slot x JOIN llm968n_sign v ON v.asset_id = x.asset_id;
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968n: % slot(s) on a sign', bad;
    END IF;
    -- The columns the art was drawn for: render_scale 1, and the anchor its
    -- feet stand on.
    SELECT count(*) INTO bad FROM asset a JOIN llm968n_sign v ON v.asset_id = a.id
     WHERE (a.render_scale, a.anchor_x, a.anchor_y)
           IS DISTINCT FROM (1::double precision, 0.5::double precision, 0.85::double precision);
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968n: % sign(s) not at render_scale 1 on anchor 0.5/0.85', bad;
    END IF;
END $$;

COMMIT;
