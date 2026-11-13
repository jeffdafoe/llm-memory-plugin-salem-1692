-- LLM-967c down: the Mana Seed chestnut and birch again. Every placed
-- Chestnut Tree 2 / 3 and Birch Tree 2 / 3 goes back to its base tree, the
-- four copy assets are deleted (their states, tags and rest default go with
-- them), and the two trees' eight states return to the Mana Seed season
-- sheets, one still frame each, in their own column (chestnut src_x 80,
-- birch src_x 160).

BEGIN;

DO $$
DECLARE
    chestnut CONSTANT uuid := '3879a83f-d32b-4b48-a108-f755c212cf0d';
    chestnut2 CONSTANT uuid := '30af7fb9-21cc-40b6-887f-1cb97b4fab86';
    chestnut3 CONSTANT uuid := 'd69cbe25-14a1-4da7-9e48-491be7bf9250';
    birch CONSTANT uuid := '1d013a1d-95c3-4d17-86e4-be18ea733528';
    birch2 CONSTANT uuid := 'e5f00663-97a0-44e8-9aa5-75149dde0c5c';
    birch3 CONSTANT uuid := '9a61e677-a795-48e5-947d-e8ba52aacf5e';
    good integer;
    want integer;
BEGIN
    UPDATE village_object SET asset_id = chestnut WHERE asset_id IN (chestnut2, chestnut3);
    UPDATE village_object SET asset_id = birch WHERE asset_id IN (birch2, birch3);

    DELETE FROM asset_state_tag WHERE state_id IN
        (SELECT id FROM asset_state WHERE asset_id IN (chestnut2, chestnut3, birch2, birch3));
    DELETE FROM asset_state WHERE asset_id IN (chestnut2, chestnut3, birch2, birch3);
    DELETE FROM asset_refresh_default WHERE asset_id IN (chestnut2, chestnut3, birch2, birch3);
    DELETE FROM asset WHERE id IN (chestnut2, chestnut3, birch2, birch3);

    CREATE TEMP TABLE llm967c_down (state text PRIMARY KEY, sheet text NOT NULL) ON COMMIT DROP;
    INSERT INTO llm967c_down VALUES
        ('default',          '/tilesets/mana-seed/summer-forest/summer sheets/summer trees 80x112.png'),
        ('autumn-clean',     '/tilesets/mana-seed/autumn-forest/autumn sheets/autumn trees (clean) 80x112.png'),
        ('autumn-leaves',    '/tilesets/mana-seed/autumn-forest/autumn sheets/autumn trees (leaves) 80x112.png'),
        ('autumn-bare',      '/tilesets/mana-seed/autumn-forest/autumn sheets/autumn trees (bare) 80x112.png'),
        ('winter-clean',     '/tilesets/mana-seed/winter-forest/winter sheets/winter trees (clean) 80x112.png'),
        ('winter-leaves',    '/tilesets/mana-seed/winter-forest/winter sheets/winter trees (leaves) 80x112.png'),
        ('winter-snowy-top', '/tilesets/mana-seed/winter-forest/winter sheets/winter trees (snowy top) 80x112.png'),
        ('winter-snowy',     '/tilesets/mana-seed/winter-forest/winter sheets/winter trees (snowy) 80x112.png');

    UPDATE asset_state s
       SET sheet = d.sheet, src_x = CASE s.asset_id WHEN chestnut THEN 80 ELSE 160 END, src_y = 0,
           src_w = 80, src_h = 112, frame_count = 1, frame_rate = 0
      FROM llm967c_down d
     WHERE s.asset_id IN (chestnut, birch) AND s.state = d.state;

    -- Each tree that exists has its eight states back on Mana Seed.
    SELECT count(*) INTO want FROM asset WHERE id IN (chestnut, birch);
    SELECT count(*) INTO good
      FROM asset_state s JOIN llm967c_down d ON d.state = s.state
     WHERE s.asset_id IN (chestnut, birch) AND s.sheet = d.sheet
       AND s.src_x = CASE s.asset_id WHEN chestnut THEN 80 ELSE 160 END
       AND s.src_y = 0 AND s.src_w = 80 AND s.src_h = 112 AND s.frame_count = 1;
    IF good <> 8 * want THEN
        RAISE EXCEPTION 'LLM-967c down: % of the % chestnut/birch states are back on Mana Seed', good, 8 * want;
    END IF;
    IF EXISTS (SELECT 1 FROM asset WHERE id IN (chestnut2, chestnut3, birch2, birch3))
       OR EXISTS (SELECT 1 FROM village_object WHERE asset_id IN (chestnut2, chestnut3, birch2, birch3)) THEN
        RAISE EXCEPTION 'LLM-967c down: a chestnut/birch copy is left behind';
    END IF;
END $$;

COMMIT;
