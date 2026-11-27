-- LLM-968: the painted debris heap.
--
-- The Debris overlay a damaged business carries by its door drew Mana Seed
-- art (public-works-debris-heap.png, composited from the pack in LLM-675b),
-- whose licence forbids use alongside AI. tools/tree-art/debris.py now paints
-- it, at twice the pack art's size and shown at render_scale 1 (the same size
-- in the world, twice the detail). Jeff approved the look on 2026-11-27,
-- after two passes of designer's review.
--
--   client/assets/trees/debris-heap.png: one row of two 128x80 frames,
--     worn  (x 0)   a board-and-batten shutter fallen flat, its corner split
--                   off, loose planks across it, two boards leaning back
--     storm (x 128) a limb torn off a tree, its butt splintered, its side
--                   branches in leaf, a smaller snapped branch in front
--
-- damage.go placeDebris picks the state from the damage trigger (storm, else
-- worn); the client's chip colours read the same state. Neither changes.
--
-- Nothing else changes: anchor 0.5/0.85 (the heap's feet are drawn there),
-- no footprint, no door, not an obstacle; fits_slot 'debris' (the slots on
-- the business buildings, LLM-675b, place it by each door); default state
-- worn. No overlay is placed now; one placed later draws the new art.
--
-- The asset, if it exists, is set, then its states are checked both ways
-- against these values. Its states carry no tags or lights and it has no
-- slots of its own: the authored sets are empty, so any row there fails the
-- migration, as does any difference in the columns the art was drawn for. A
-- missing state is not recreated; the check fails on it. A database without
-- the asset takes no part.
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
    debris CONSTANT uuid := '019e5f00-c401-7a10-9e00-000000675001';
BEGIN
    IF NOT EXISTS (SELECT 1 FROM asset WHERE id = debris) THEN
        RETURN;
    END IF;

    -- Every state, as authored.
    CREATE TEMP TABLE llm968q_state (asset_id uuid, state text, sheet text, src_x integer, src_y integer,
                                     src_w integer, src_h integer, frame_count integer,
                                     frame_rate double precision) ON COMMIT DROP;
    INSERT INTO llm968q_state VALUES
        ('019e5f00-c401-7a10-9e00-000000675001', 'worn', 'res://assets/trees/debris-heap.png', 0, 0, 128, 80, 1, 0),
        ('019e5f00-c401-7a10-9e00-000000675001', 'storm', 'res://assets/trees/debris-heap.png', 128, 0, 128, 80, 1, 0);

    UPDATE asset SET render_scale = 1 WHERE id = debris;
    -- Existing states only: the check below fails on a missing one.
    UPDATE asset_state s
       SET sheet = v.sheet, src_x = v.src_x, src_y = v.src_y, src_w = v.src_w, src_h = v.src_h,
           frame_count = v.frame_count, frame_rate = v.frame_rate
      FROM llm968q_state v
     WHERE s.asset_id = v.asset_id AND s.state = v.state;

    SELECT count(*) INTO bad FROM (
        (SELECT asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate FROM llm968q_state
         EXCEPT
         SELECT asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate
           FROM asset_state WHERE asset_id = debris)
        UNION ALL
        (SELECT asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate
           FROM asset_state WHERE asset_id = debris
         EXCEPT
         SELECT asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate FROM llm968q_state)
    ) diff;
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968q: % debris state rows differ from the authored values', bad;
    END IF;
    SELECT count(*) INTO bad
      FROM asset_state s
     WHERE s.asset_id = debris
       AND (EXISTS (SELECT 1 FROM asset_state_tag t WHERE t.state_id = s.id)
            OR EXISTS (SELECT 1 FROM asset_state_light l WHERE l.state_id = s.id));
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968q: % debris state(s) carry a tag or a light', bad;
    END IF;
    SELECT count(*) INTO bad FROM asset_slot WHERE asset_id = debris;
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968q: % slot(s) on the debris heap', bad;
    END IF;
    -- The columns the art was drawn for: render_scale 1, the anchor its feet
    -- stand on, no footprint, no door, not an obstacle or a passage, the
    -- debris slot it fits, worn by default.
    SELECT count(*) INTO bad FROM asset a
     WHERE a.id = debris
       AND (a.render_scale, a.anchor_x, a.anchor_y, a.footprint_left, a.footprint_right, a.footprint_top,
            a.footprint_bottom, a.door_offset_x, a.door_offset_y, a.is_obstacle, a.is_passage,
            a.default_state::text, a.fits_slot::text)
           IS DISTINCT FROM (1::double precision, 0.5::double precision, 0.85::double precision, 0, 0, 0, 0,
                             NULL::integer, NULL::integer, false, false, 'worn'::text, 'debris'::text);
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968q: the debris heap differs from the columns the art was drawn for';
    END IF;
END $$;

COMMIT;
