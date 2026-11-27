-- LLM-968: painted millstone.
--
-- The Millstone drew Mana Seed village accessories 32x32.png, whose licence
-- forbids use alongside AI. tools/tree-art/millstone.py now paints it:
-- client/assets/trees/millstone.png (one 64x64 frame), shown at render_scale
-- 1: a pair of spare millstones, the bed stone lying flat and the runner
-- stood on edge behind it. Jeff approved the look on 2026-11-27.
--
-- ASSET (live-only: no migration creates it): render_scale 1 and the one
-- default state on the new sheet. Nothing else changes: anchor 0.5/0.85, no
-- footprint, not an obstacle, no door; the one placement (by the Mill) stays
-- where it is.
--
-- The asset is set, then checked exactly (render_scale and the columns it
-- keeps, its one state, no tags, lights or slots); any difference fails the
-- migration. ENGINE-OWNED data, read at boot; deploy.sh runs stop ->
-- migrate -> start. Convergent: a rerun ends in the same place and repairs a
-- hand edit.

BEGIN;

LOCK TABLE asset, asset_state, asset_state_tag, asset_state_light, asset_slot IN SHARE ROW EXCLUSIVE MODE;

DO $$
DECLARE
    stone CONSTANT uuid := '6c3fe5e1-bd95-4080-9735-7f90cad36199';
BEGIN
    IF NOT EXISTS (SELECT 1 FROM asset WHERE id = stone) THEN
        RETURN;
    END IF;

    UPDATE asset SET render_scale = 1 WHERE id = stone;
    -- The existing default state only: the check below fails on a missing or
    -- extra one.
    UPDATE asset_state
       SET sheet = 'res://assets/trees/millstone.png', src_x = 0, src_y = 0, src_w = 64, src_h = 64,
           frame_count = 1, frame_rate = 0
     WHERE asset_id = stone AND state = 'default';

    IF (SELECT count(*) FROM asset_state WHERE asset_id = stone) <> 1
       OR EXISTS (SELECT 1 FROM asset_state s WHERE s.asset_id = stone
                   AND (s.state::text, s.sheet::text, s.src_x, s.src_y, s.src_w, s.src_h, s.frame_count, s.frame_rate)
                       IS DISTINCT FROM ('default'::text, 'res://assets/trees/millstone.png'::text, 0, 0, 64, 64, 1,
                                         0::double precision)) THEN
        RAISE EXCEPTION 'LLM-968l: the millstone does not have exactly its one authored default state';
    END IF;
    IF EXISTS (SELECT 1 FROM asset_state s WHERE s.asset_id = stone
                AND (EXISTS (SELECT 1 FROM asset_state_tag t WHERE t.state_id = s.id)
                     OR EXISTS (SELECT 1 FROM asset_state_light l WHERE l.state_id = s.id))) THEN
        RAISE EXCEPTION 'LLM-968l: the millstone state carries a tag or a light';
    END IF;
    -- The columns the art was drawn for: the anchor it stands on, and no
    -- footprint (the stones sit on their anchor tile, walkable).
    IF EXISTS (SELECT 1 FROM asset a WHERE a.id = stone
                AND ((a.render_scale, a.anchor_x, a.anchor_y, a.footprint_left, a.footprint_right, a.footprint_top,
                      a.footprint_bottom, a.door_offset_x, a.door_offset_y, a.is_obstacle, a.is_passage,
                      a.default_state::text)
                     IS DISTINCT FROM (1::double precision, 0.5::double precision, 0.85::double precision, 0, 0, 0, 0,
                                       NULL::integer, NULL::integer, false, false, 'default'::text)
                     OR EXISTS (SELECT 1 FROM asset_slot sl WHERE sl.asset_id = a.id))) THEN
        RAISE EXCEPTION 'LLM-968l: the millstone asset differs from the columns its art was drawn for';
    END IF;
END $$;

COMMIT;
