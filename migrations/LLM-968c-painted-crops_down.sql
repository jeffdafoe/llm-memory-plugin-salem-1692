-- LLM-968 down: Wheat and Carrots back on the Mana Seed farming-crops sheets,
-- 16x32 frames at render_scale 2, as LLM-576 and LLM-603 set them: growth-N
-- at x = 16 * (N + 1) on its crop's sheet. Tags were not changed by the up
-- and are not touched here. Convergent, and a crop not in this database takes
-- no part.

BEGIN;

DO $$
DECLARE
    bad integer;
BEGIN
    CREATE TEMP TABLE llm968c_down (asset_id uuid PRIMARY KEY, sheet text) ON COMMIT DROP;
    INSERT INTO llm968c_down VALUES
        ('019e5f00-c401-7a10-9e00-000000000576',
         '/tilesets/mana-seed/farming-crops-1/sheets by crop/farming crops (wheat) 16x32.png'),   -- Wheat
        ('019e5f00-c401-7a10-9e00-000000000603',
         '/tilesets/mana-seed/farming-crops-1/sheets by crop/farming crops (carrot) 16x32.png');  -- Carrots
    DELETE FROM llm968c_down v WHERE NOT EXISTS (SELECT 1 FROM asset WHERE id = v.asset_id);
    IF NOT EXISTS (SELECT 1 FROM llm968c_down) THEN
        RETURN;
    END IF;

    CREATE TEMP TABLE llm968c_down_state ON COMMIT DROP AS
    SELECT v.asset_id, 'growth-' || n AS state, v.sheet, (n + 1) * 16 AS src_x, 0 AS src_y, 16 AS src_w, 32 AS src_h,
           1 AS frame_count, 0::double precision AS frame_rate
      FROM llm968c_down v CROSS JOIN generate_series(1, 6) n;

    UPDATE asset_state s
       SET sheet = v.sheet, src_x = v.src_x, src_y = v.src_y, src_w = v.src_w, src_h = v.src_h,
           frame_count = v.frame_count, frame_rate = v.frame_rate
      FROM llm968c_down_state v
     WHERE s.asset_id = v.asset_id AND s.state = v.state;
    UPDATE asset SET render_scale = 2 WHERE id IN (SELECT asset_id FROM llm968c_down);

    -- Every crop has exactly its six states, every value as before LLM-968c.
    SELECT count(*) INTO bad FROM (
        (SELECT asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate FROM llm968c_down_state
         EXCEPT
         SELECT s.asset_id, s.state, s.sheet, s.src_x, s.src_y, s.src_w, s.src_h, s.frame_count, s.frame_rate
           FROM asset_state s JOIN llm968c_down v ON v.asset_id = s.asset_id)
        UNION ALL
        (SELECT s.asset_id, s.state, s.sheet, s.src_x, s.src_y, s.src_w, s.src_h, s.frame_count, s.frame_rate
           FROM asset_state s JOIN llm968c_down v ON v.asset_id = s.asset_id
         EXCEPT
         SELECT asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate FROM llm968c_down_state)
    ) diff;
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968c down: % crop state rows differ from the Mana Seed values', bad;
    END IF;
    SELECT count(*) INTO bad FROM asset WHERE id IN (SELECT asset_id FROM llm968c_down) AND render_scale <> 2;
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968c down: % crop(s) not back at render_scale 2', bad;
    END IF;
END $$;

COMMIT;
