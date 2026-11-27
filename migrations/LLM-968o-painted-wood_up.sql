-- LLM-968: painted woodpiles: Wood Pile (Large) and the Wood Shelter.
--
-- They drew Mana Seed village-accessories art, whose licence forbids use
-- alongside AI. tools/tree-art/wood.py now paints them, at twice the pack
-- art's size and shown at render_scale 1 (the same size in the world, twice
-- the detail). Jeff approved the look on 2026-11-27, after two passes of
-- designer's review.
--
--   client/assets/trees/wood-pile.png: one 64x64 frame, a domed rick of
--     split cordwood seen end-on.
--   client/assets/trees/wood-shelter.png: one 64x64 frame, an open woodshed
--     seen corner-on with cordwood stacked inside.
--
-- Nothing else changes: anchors 0.5/0.85 (the art's feet are drawn there),
-- footprints (the pile none, the shelter top 2), no door, not obstacles; the
-- pile's firewood refresh default is untouched, and the five placements stay
-- where they are.
--
-- Each asset that exists is set, then its one state is checked both ways
-- against these values, and it must have no tags, lights or slots and keep
-- the columns the art was drawn for; any difference fails the migration. A
-- missing state is not recreated; the check fails on it. An asset not in
-- this database takes no part (no migration creates either).
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
    -- Each asset with the columns its art is drawn for: footprint left,
    -- right, top, bottom.
    CREATE TEMP TABLE llm968o_asset (asset_id uuid PRIMARY KEY, fl integer, fr integer, ft integer, fb integer)
        ON COMMIT DROP;
    INSERT INTO llm968o_asset VALUES
        ('74881601-00fd-4c07-898b-a9d3c155b4cc', 0, 0, 0, 0),   -- Wood Pile (Large)
        ('57229387-1773-4bf8-89f0-3dc0b090ed4d', 0, 0, 2, 0);   -- Wood Shelter
    DELETE FROM llm968o_asset v WHERE NOT EXISTS (SELECT 1 FROM asset WHERE id = v.asset_id);
    IF NOT EXISTS (SELECT 1 FROM llm968o_asset) THEN
        RETURN;
    END IF;

    -- Every state, as authored.
    CREATE TEMP TABLE llm968o_state (asset_id uuid, state text, sheet text, src_x integer, src_y integer,
                                     src_w integer, src_h integer, frame_count integer,
                                     frame_rate double precision) ON COMMIT DROP;
    INSERT INTO llm968o_state VALUES
        ('74881601-00fd-4c07-898b-a9d3c155b4cc', 'default', 'res://assets/trees/wood-pile.png', 0, 0, 64, 64, 1, 0),
        ('57229387-1773-4bf8-89f0-3dc0b090ed4d', 'default', 'res://assets/trees/wood-shelter.png', 0, 0, 64, 64, 1, 0);
    DELETE FROM llm968o_state v WHERE NOT EXISTS (SELECT 1 FROM llm968o_asset l WHERE l.asset_id = v.asset_id);

    UPDATE asset a SET render_scale = 1 FROM llm968o_asset v WHERE a.id = v.asset_id;
    -- Existing states only: the check below fails on a missing one.
    UPDATE asset_state s
       SET sheet = v.sheet, src_x = v.src_x, src_y = v.src_y, src_w = v.src_w, src_h = v.src_h,
           frame_count = v.frame_count, frame_rate = v.frame_rate
      FROM llm968o_state v
     WHERE s.asset_id = v.asset_id AND s.state = v.state;

    SELECT count(*) INTO bad FROM (
        (SELECT asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate FROM llm968o_state
         EXCEPT
         SELECT s.asset_id, s.state, s.sheet, s.src_x, s.src_y, s.src_w, s.src_h, s.frame_count, s.frame_rate
           FROM asset_state s JOIN llm968o_asset v ON v.asset_id = s.asset_id)
        UNION ALL
        (SELECT s.asset_id, s.state, s.sheet, s.src_x, s.src_y, s.src_w, s.src_h, s.frame_count, s.frame_rate
           FROM asset_state s JOIN llm968o_asset v ON v.asset_id = s.asset_id
         EXCEPT
         SELECT asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate FROM llm968o_state)
    ) diff;
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968o: % woodpile state rows differ from the authored values', bad;
    END IF;
    SELECT count(*) INTO bad
      FROM asset_state s JOIN llm968o_asset v ON v.asset_id = s.asset_id
     WHERE EXISTS (SELECT 1 FROM asset_state_tag t WHERE t.state_id = s.id)
        OR EXISTS (SELECT 1 FROM asset_state_light l WHERE l.state_id = s.id);
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968o: % woodpile state(s) carry a tag or a light', bad;
    END IF;
    SELECT count(*) INTO bad FROM asset_slot x JOIN llm968o_asset v ON v.asset_id = x.asset_id;
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968o: % slot(s) on a woodpile', bad;
    END IF;
    -- The columns the art was drawn for: render_scale 1, the anchor its feet
    -- stand on, its footprint, no door, not an obstacle or a passage.
    SELECT count(*) INTO bad FROM asset a JOIN llm968o_asset v ON v.asset_id = a.id
     WHERE (a.render_scale, a.anchor_x, a.anchor_y, a.footprint_left, a.footprint_right, a.footprint_top,
            a.footprint_bottom, a.door_offset_x, a.door_offset_y, a.is_obstacle, a.is_passage, a.default_state::text)
           IS DISTINCT FROM (1::double precision, 0.5::double precision, 0.85::double precision, v.fl, v.fr, v.ft,
                             v.fb, NULL::integer, NULL::integer, false, false, 'default'::text);
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968o: % woodpile(s) differ from the columns the art was drawn for', bad;
    END IF;
END $$;

COMMIT;
