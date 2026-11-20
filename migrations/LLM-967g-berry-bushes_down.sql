-- LLM-967g down: the Mana Seed bushes again. Every placed copy (Blueberry Bush
-- 2 / 3, Raspberry Bush 2 / 3, Sage Bush 2 / 3) goes back to its bush (keeping
-- its berries left), the six copy assets are deleted (their states, tags and
-- rest default go with them), a bush showing a winter state moves to its autumn
-- counterpart (winter-berries and winter-snowy-berries to autumn-berries, the
-- bare ones to autumn-bare, the sage's to autumn), the winter states and their
-- tags are deleted, and the summer and autumn states return to the Mana Seed
-- forest sheets, one still frame each.

BEGIN;

DO $$
DECLARE
    blueberry CONSTANT uuid := '630909ca-df4f-43ac-9fc4-5192ca44da73';
    raspberry CONSTANT uuid := 'db4b428c-9ab6-4457-85fb-3f85fe86c946';
    sage CONSTANT uuid := 'bad3e45f-3649-4cf2-b9b2-cda95b334113';
    copies CONSTANT uuid[] := ARRAY['06a3858b-d75e-44e2-a896-a29cdf0a09a7', '1c378426-eb00-4fb9-a474-b6e61e57f925',
                                    '67e5be5e-d955-4d6b-b119-ad40f0c51f5e', 'e6ac2605-cd55-484b-ba4a-857686219df9',
                                    '55e56e90-55f0-4d53-88d5-5e2b9a342dca', 'a48e0653-24ee-4009-a303-b19558c0b233']::uuid[];
    summer16x32 CONSTANT text := '/tilesets/mana-seed/summer-forest/summer sheets/summer 16x32.png';
    autumn16x32 CONSTANT text := '/tilesets/mana-seed/autumn-forest/autumn sheets/autumn 16x32.png';
    summer32x32 CONSTANT text := '/tilesets/mana-seed/summer-forest/summer sheets/summer 32x32.png';
    autumn32x32 CONSTANT text := '/tilesets/mana-seed/autumn-forest/autumn sheets/autumn 32x32.png';
    summer16x16 CONSTANT text := '/tilesets/mana-seed/summer-forest/summer sheets/summer 16x16.png';
    autumn16x16 CONSTANT text := '/tilesets/mana-seed/autumn-forest/autumn sheets/autumn 16x16.png';
    winter CONSTANT text[] := ARRAY['winter-berries', 'winter-bare', 'winter-snowy-berries', 'winter-snowy-bare',
                                    'winter', 'winter-snowy'];
    good integer;
BEGIN
    -- A database with none of the bushes has no catalog (the up did nothing).
    IF NOT EXISTS (SELECT 1 FROM asset WHERE id IN (blueberry, raspberry, sage)) THEN
        RETURN;
    END IF;

    -- Each copy's placements back to its bush.
    UPDATE village_object SET asset_id = blueberry
     WHERE asset_id IN ('06a3858b-d75e-44e2-a896-a29cdf0a09a7', '1c378426-eb00-4fb9-a474-b6e61e57f925');
    UPDATE village_object SET asset_id = raspberry
     WHERE asset_id IN ('67e5be5e-d955-4d6b-b119-ad40f0c51f5e', 'e6ac2605-cd55-484b-ba4a-857686219df9');
    UPDATE village_object SET asset_id = sage
     WHERE asset_id IN ('55e56e90-55f0-4d53-88d5-5e2b9a342dca', 'a48e0653-24ee-4009-a303-b19558c0b233');

    DELETE FROM asset_state_tag WHERE state_id IN (SELECT id FROM asset_state WHERE asset_id = ANY (copies));
    DELETE FROM asset_state WHERE asset_id = ANY (copies);
    DELETE FROM asset_refresh_default WHERE asset_id = ANY (copies);
    DELETE FROM asset WHERE id = ANY (copies);

    -- No placed bush may point at a winter state that is about to go.
    UPDATE village_object
       SET current_state = CASE
               WHEN asset_id = sage THEN 'autumn'
               WHEN current_state LIKE '%-berries' THEN 'autumn-berries'
               ELSE 'autumn-bare' END
     WHERE asset_id IN (blueberry, raspberry, sage) AND current_state = ANY (winter);

    DELETE FROM asset_state_tag
     WHERE state_id IN (SELECT id FROM asset_state WHERE asset_id IN (blueberry, raspberry, sage) AND state = ANY (winter));
    DELETE FROM asset_state WHERE asset_id IN (blueberry, raspberry, sage) AND state = ANY (winter);

    -- The Mana Seed sheets, as they were before LLM-967g.
    CREATE TEMP TABLE llm967g_down (asset_id uuid NOT NULL, state text NOT NULL, sheet text NOT NULL,
                                    src_x integer NOT NULL, src_y integer NOT NULL, src_w integer NOT NULL,
                                    src_h integer NOT NULL, PRIMARY KEY (asset_id, state)) ON COMMIT DROP;
    INSERT INTO llm967g_down VALUES
        (blueberry, 'berries',        summer16x32, 80,  0, 16, 32),
        (blueberry, 'bare',           summer16x32, 64,  0, 16, 32),
        (blueberry, 'autumn-berries', autumn16x32, 80,  0, 16, 32),
        (blueberry, 'autumn-bare',    autumn16x32, 64,  0, 16, 32),
        (raspberry, 'berries',        summer32x32, 32,  0, 32, 32),
        (raspberry, 'bare',           summer32x32,  0,  0, 32, 32),
        (raspberry, 'autumn-berries', autumn32x32, 32,  0, 32, 32),
        (raspberry, 'autumn-bare',    autumn32x32,  0,  0, 32, 32),
        (sage,      'default',        summer16x16,  0, 64, 16, 16),
        (sage,      'autumn',         autumn16x16,  0, 64, 16, 16);

    UPDATE asset_state s
       SET sheet = d.sheet, src_x = d.src_x, src_y = d.src_y, src_w = d.src_w, src_h = d.src_h,
           frame_count = 1, frame_rate = 0
      FROM llm967g_down d
     WHERE s.asset_id = d.asset_id AND s.state = d.state;

    -- Every bush that exists is back on Mana Seed, and nothing of LLM-967g is left.
    SELECT count(*) INTO good
      FROM asset_state s JOIN llm967g_down d ON d.asset_id = s.asset_id AND d.state = s.state
     WHERE s.sheet = d.sheet AND s.src_x = d.src_x AND s.src_y = d.src_y AND s.src_w = d.src_w
       AND s.src_h = d.src_h AND s.frame_count = 1 AND s.frame_rate = 0;
    IF good <> (SELECT count(*) FROM llm967g_down d JOIN asset a ON a.id = d.asset_id) THEN
        RAISE EXCEPTION 'LLM-967g down: only % bush states are back on Mana Seed', good;
    END IF;
    IF EXISTS (SELECT 1 FROM asset WHERE id = ANY (copies))
       OR EXISTS (SELECT 1 FROM village_object WHERE asset_id = ANY (copies))
       OR EXISTS (SELECT 1 FROM asset_state WHERE asset_id IN (blueberry, raspberry, sage) AND state = ANY (winter)) THEN
        RAISE EXCEPTION 'LLM-967g down: a bush copy or winter state is left behind';
    END IF;
END $$;

COMMIT;
