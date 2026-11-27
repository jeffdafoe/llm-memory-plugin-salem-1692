-- LLM-968: painted outhouse: Outhouse 1.
--
-- It drew Mana Seed village-accessories art, whose licence forbids use
-- alongside AI. tools/tree-art/outhouse.py now paints it: client/assets/
-- trees/outhouse.png, one 64x128 frame, at twice the pack art's size and
-- shown at render_scale 1 (the same size in the world, twice the detail): a
-- 1690s board-and-batten privy under a riven-board shed roof, its door to
-- the front. Jeff approved the look on 2026-11-27, after designer's review
-- (two passes, and a third on the reworked roof).
--
-- Nothing else changes. The art fills the frame as the pack art did, so the
-- columns a player's visit depends on stay: anchor 0.5/0.85, footprint L1 R1
-- T3 B0, an obstacle, the door at the anchor tile (0, 0). The two placements
-- keep their places and their `outhouse` instance tag (village_object, not
-- touched here), which makes them outhouses (engine/sim/outhouse.go).
--
-- The asset, if it exists, is set, then its one state is checked both ways
-- against these values, and it must have no tags, lights or slots and keep
-- the columns above; any difference fails the migration. A missing state is
-- not recreated; the check fails on it. A database without the asset (no
-- migration creates it) takes no part.
--
-- ENGINE-OWNED data (the catalog), read at boot; deploy.sh runs stop ->
-- migrate -> start. Convergent: a rerun ends in the same place and repairs a
-- hand edit of the rect.

BEGIN;

-- Hold off any other catalog writer (the editor) between the presence check
-- and the checks below.
LOCK TABLE asset, asset_state, asset_state_tag, asset_state_light, asset_slot IN SHARE ROW EXCLUSIVE MODE;

DO $$
DECLARE
    outhouse CONSTANT uuid := '053ec0f2-79bd-4db6-a9df-fec642792244';
    bad integer;
BEGIN
    IF NOT EXISTS (SELECT 1 FROM asset WHERE id = outhouse) THEN
        RETURN;
    END IF;

    -- Every state, as authored.
    CREATE TEMP TABLE llm968p_state (asset_id uuid, state text, sheet text, src_x integer, src_y integer,
                                     src_w integer, src_h integer, frame_count integer,
                                     frame_rate double precision) ON COMMIT DROP;
    INSERT INTO llm968p_state VALUES
        ('053ec0f2-79bd-4db6-a9df-fec642792244', 'default', 'res://assets/trees/outhouse.png', 0, 0, 64, 128, 1, 0);

    UPDATE asset SET render_scale = 1 WHERE id = outhouse;
    -- The existing state only: the check below fails on a missing or extra one.
    UPDATE asset_state s
       SET sheet = v.sheet, src_x = v.src_x, src_y = v.src_y, src_w = v.src_w, src_h = v.src_h,
           frame_count = v.frame_count, frame_rate = v.frame_rate
      FROM llm968p_state v
     WHERE s.asset_id = v.asset_id AND s.state = v.state;

    SELECT count(*) INTO bad FROM (
        (SELECT asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate FROM llm968p_state
         EXCEPT
         SELECT s.asset_id, s.state, s.sheet, s.src_x, s.src_y, s.src_w, s.src_h, s.frame_count, s.frame_rate
           FROM asset_state s WHERE s.asset_id = outhouse)
        UNION ALL
        (SELECT s.asset_id, s.state, s.sheet, s.src_x, s.src_y, s.src_w, s.src_h, s.frame_count, s.frame_rate
           FROM asset_state s WHERE s.asset_id = outhouse
         EXCEPT
         SELECT asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate FROM llm968p_state)
    ) diff;
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968p: % outhouse state rows differ from the authored values', bad;
    END IF;
    IF EXISTS (SELECT 1 FROM asset_state s WHERE s.asset_id = outhouse
                AND (EXISTS (SELECT 1 FROM asset_state_tag t WHERE t.state_id = s.id)
                     OR EXISTS (SELECT 1 FROM asset_state_light l WHERE l.state_id = s.id))) THEN
        RAISE EXCEPTION 'LLM-968p: the outhouse state carries a tag or a light';
    END IF;
    IF EXISTS (SELECT 1 FROM asset_slot WHERE asset_id = outhouse) THEN
        RAISE EXCEPTION 'LLM-968p: the outhouse has a slot';
    END IF;
    -- The columns the art and a visit depend on.
    IF EXISTS (SELECT 1 FROM asset a WHERE a.id = outhouse
                AND (a.render_scale, a.anchor_x, a.anchor_y, a.footprint_left, a.footprint_right, a.footprint_top,
                     a.footprint_bottom, a.door_offset_x, a.door_offset_y, a.is_obstacle, a.is_passage,
                     a.default_state::text)
                    IS DISTINCT FROM (1::double precision, 0.5::double precision, 0.85::double precision, 1, 1, 3, 0,
                                      0, 0, true, false, 'default'::text)) THEN
        RAISE EXCEPTION 'LLM-968p: the outhouse differs from the columns its art and its visits depend on';
    END IF;
END $$;

COMMIT;
