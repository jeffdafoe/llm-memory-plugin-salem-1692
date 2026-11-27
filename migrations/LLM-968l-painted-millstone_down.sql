-- LLM-968 down: the Millstone back on Mana Seed village accessories
-- 32x32.png (64, 32, 32x32) at render_scale 2. Convergent.

BEGIN;

LOCK TABLE asset, asset_state IN SHARE ROW EXCLUSIVE MODE;

DO $$
DECLARE
    stone CONSTANT uuid := '6c3fe5e1-bd95-4080-9735-7f90cad36199';
    pack CONSTANT text := '/tilesets/mana-seed/village-accessories/village accessories 32x32.png';
BEGIN
    IF NOT EXISTS (SELECT 1 FROM asset WHERE id = stone) THEN
        RETURN;
    END IF;

    UPDATE asset SET render_scale = 2 WHERE id = stone;
    UPDATE asset_state
       SET sheet = pack, src_x = 64, src_y = 32, src_w = 32, src_h = 32, frame_count = 1, frame_rate = 0
     WHERE asset_id = stone AND state = 'default';

    IF (SELECT count(*) FROM asset_state WHERE asset_id = stone) <> 1
       OR EXISTS (SELECT 1 FROM asset_state s WHERE s.asset_id = stone
                   AND (s.state::text, s.sheet::text, s.src_x, s.src_y, s.src_w, s.src_h, s.frame_count, s.frame_rate)
                       IS DISTINCT FROM ('default'::text, pack, 64, 32, 32, 32, 1, 0::double precision))
       OR EXISTS (SELECT 1 FROM asset WHERE id = stone AND render_scale IS DISTINCT FROM 2::double precision) THEN
        RAISE EXCEPTION 'LLM-968l down: the millstone differs from the pack values';
    END IF;
END $$;

COMMIT;
