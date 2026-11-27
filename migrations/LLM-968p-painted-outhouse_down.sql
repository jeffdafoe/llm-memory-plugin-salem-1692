-- LLM-968 down: Outhouse 1 back on the Mana Seed village accessories
-- 32x64.png rect (0, 0, 32x64) at render_scale 2, as the pack catalog set
-- it. The anchor, footprint, door and obstacle flag the up requires are
-- checked here too (the pack art stands on them as well). Convergent.

BEGIN;

LOCK TABLE asset, asset_state IN SHARE ROW EXCLUSIVE MODE;

DO $$
DECLARE
    outhouse CONSTANT uuid := '053ec0f2-79bd-4db6-a9df-fec642792244';
    pack CONSTANT text := '/tilesets/mana-seed/village-accessories/village accessories 32x64.png';
BEGIN
    IF NOT EXISTS (SELECT 1 FROM asset WHERE id = outhouse) THEN
        RETURN;
    END IF;

    UPDATE asset SET render_scale = 2 WHERE id = outhouse;
    UPDATE asset_state
       SET sheet = pack, src_x = 0, src_y = 0, src_w = 32, src_h = 64, frame_count = 1, frame_rate = 0
     WHERE asset_id = outhouse AND state = 'default';

    IF (SELECT count(*) FROM asset_state WHERE asset_id = outhouse) <> 1
       OR EXISTS (SELECT 1 FROM asset_state s WHERE s.asset_id = outhouse
                   AND (s.state::text, s.sheet::text, s.src_x, s.src_y, s.src_w, s.src_h, s.frame_count, s.frame_rate)
                       IS DISTINCT FROM ('default'::text, pack, 0, 0, 32, 64, 1, 0::double precision)) THEN
        RAISE EXCEPTION 'LLM-968p down: the outhouse state differs from the pack values';
    END IF;
    IF EXISTS (SELECT 1 FROM asset a WHERE a.id = outhouse
                AND (a.render_scale, a.anchor_x, a.anchor_y, a.footprint_left, a.footprint_right, a.footprint_top,
                     a.footprint_bottom, a.door_offset_x, a.door_offset_y, a.is_obstacle)
                    IS DISTINCT FROM (2::double precision, 0.5::double precision, 0.85::double precision, 1, 1, 3, 0,
                                      0, 0, true)) THEN
        RAISE EXCEPTION 'LLM-968p down: the outhouse is not back at render_scale 2 on its anchor, footprint and door';
    END IF;
END $$;

COMMIT;
