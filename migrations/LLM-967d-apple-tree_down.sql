-- LLM-967d down: the Mana Seed apple tree again. Every placed Apple Tree 2 / 3
-- goes back to the Apple Tree (keeping its current state and apples left),
-- the two variant assets are deleted (their states, tags and rest default go
-- with them), and the Apple Tree's four states return to the Mana Seed fruit
-- tree sheet, one still 48x64 frame each: the summer row (src_y 64) and the
-- autumn row (src_y 128), the leafy tree at src_x 144, with apples at 192.

BEGIN;

DO $$
DECLARE
    apple CONSTANT uuid := '019e5f00-c401-7a10-9e00-000000000623';
    apple2 CONSTANT uuid := '48f8b1e5-a9e6-45d7-87f9-acbbf83fcc61';
    apple3 CONSTANT uuid := 'e8d24766-7ef7-4380-8ff4-0e4de2d9cb05';
    ms_sheet CONSTANT text := '/tilesets/mana-seed/growable-fruit-trees/fruit trees, shadow (apple, red) 48x64.png';
    good integer;
BEGIN
    IF NOT EXISTS (SELECT 1 FROM asset WHERE id = apple) THEN
        RETURN;
    END IF;

    UPDATE village_object SET asset_id = apple WHERE asset_id IN (apple2, apple3);

    DELETE FROM asset_state_tag WHERE state_id IN (SELECT id FROM asset_state WHERE asset_id IN (apple2, apple3));
    DELETE FROM asset_state WHERE asset_id IN (apple2, apple3);
    DELETE FROM asset_refresh_default WHERE asset_id IN (apple2, apple3);
    DELETE FROM asset WHERE id IN (apple2, apple3);

    CREATE TEMP TABLE llm967d_down (state text PRIMARY KEY, src_x integer NOT NULL, src_y integer NOT NULL) ON COMMIT DROP;
    INSERT INTO llm967d_down VALUES
        ('berries', 192, 64), ('bare', 144, 64), ('autumn-berries', 192, 128), ('autumn-bare', 144, 128);

    UPDATE asset_state s
       SET sheet = ms_sheet, src_x = d.src_x, src_y = d.src_y, src_w = 48, src_h = 64,
           frame_count = 1, frame_rate = 0
      FROM llm967d_down d
     WHERE s.asset_id = apple AND s.state = d.state;

    SELECT count(*) INTO good
      FROM asset_state s JOIN llm967d_down d ON d.state = s.state
     WHERE s.asset_id = apple AND s.sheet = ms_sheet AND s.src_x = d.src_x AND s.src_y = d.src_y
       AND s.src_w = 48 AND s.src_h = 64 AND s.frame_count = 1;
    IF good <> 4 THEN
        RAISE EXCEPTION 'LLM-967d down: % of the 4 Apple Tree states are back on Mana Seed', good;
    END IF;
    IF EXISTS (SELECT 1 FROM asset WHERE id IN (apple2, apple3))
       OR EXISTS (SELECT 1 FROM village_object WHERE asset_id IN (apple2, apple3)) THEN
        RAISE EXCEPTION 'LLM-967d down: an apple tree variant is left behind';
    END IF;
END $$;

COMMIT;
