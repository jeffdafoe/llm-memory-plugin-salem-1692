-- LLM-967 down: the Maple Tree's eight states back on the Mana Seed forest
-- sheets (as LLM-849 left them: each look its season's sheet, the maple at
-- the sheet's top-left 80x112).

BEGIN;

DO $$
DECLARE
    maple CONSTANT uuid := '2d91c8a9-6501-4e16-873a-4d18bdc6f63e';
    good integer;
BEGIN
    IF NOT EXISTS (SELECT 1 FROM asset WHERE id = maple) THEN
        RETURN;
    END IF;

    CREATE TEMP TABLE llm967_maple_down (state text PRIMARY KEY, old_sheet text NOT NULL) ON COMMIT DROP;
    INSERT INTO llm967_maple_down (state, old_sheet) VALUES
        ('default',          '/tilesets/mana-seed/summer-forest/summer sheets/summer trees 80x112.png'),
        ('autumn-clean',     '/tilesets/mana-seed/autumn-forest/autumn sheets/autumn trees (clean) 80x112.png'),
        ('autumn-leaves',    '/tilesets/mana-seed/autumn-forest/autumn sheets/autumn trees (leaves) 80x112.png'),
        ('autumn-bare',      '/tilesets/mana-seed/autumn-forest/autumn sheets/autumn trees (bare) 80x112.png'),
        ('winter-clean',     '/tilesets/mana-seed/winter-forest/winter sheets/winter trees (clean) 80x112.png'),
        ('winter-leaves',    '/tilesets/mana-seed/winter-forest/winter sheets/winter trees (leaves) 80x112.png'),
        ('winter-snowy-top', '/tilesets/mana-seed/winter-forest/winter sheets/winter trees (snowy top) 80x112.png'),
        ('winter-snowy',     '/tilesets/mana-seed/winter-forest/winter sheets/winter trees (snowy) 80x112.png');

    UPDATE asset_state s
       SET sheet = m.old_sheet, src_x = 0, src_y = 0, src_w = 80, src_h = 112,
           frame_count = 1, frame_rate = 0
      FROM llm967_maple_down m
     WHERE s.asset_id = maple AND s.state = m.state;

    SELECT count(*) INTO good
      FROM asset_state s
      JOIN llm967_maple_down m ON m.state = s.state
     WHERE s.asset_id = maple AND s.sheet = m.old_sheet AND s.src_x = 0 AND s.src_y = 0
       AND s.src_w = 80 AND s.src_h = 112;
    IF good <> 8 THEN
        RAISE EXCEPTION 'LLM-967 down: % of the 8 Maple Tree states are back on the Mana Seed sheets', good;
    END IF;
END $$;

COMMIT;
