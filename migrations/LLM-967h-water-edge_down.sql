-- LLM-967h down: the Mana Seed water edge again. Every placed Cattails 2 / 3
-- and Water Plant 2 / 3 goes back to its clump, the four copy assets are
-- deleted (their states, tags and rest default go with them), and every
-- water-edge state returns to its Mana Seed forest or village-accessories rect,
-- one still frame each. No state was added or removed by the up, so none is
-- removed here.

BEGIN;

DO $$
DECLARE
    cattails CONSTANT uuid := '50be81a0-81ef-48e8-b078-56bd075ee261';
    water_plant CONSTANT uuid := 'cb4fb077-1199-4c24-b1a8-583dd9568762';
    lily_pad CONSTANT uuid := '50146fff-7bb4-4805-a65a-1e1fd4c9e97c';
    light_lily_pad CONSTANT uuid := 'bf824093-528d-4cec-9d9a-f2ac07f6a1dd';
    small_lily_pad CONSTANT uuid := '55d4951b-0400-4588-a9d9-3274c9138cf6';
    tiny_lily_pad CONSTANT uuid := 'e78bd2e8-6932-4312-bff0-25412474f461';
    water_vine CONSTANT uuid := 'ab4a39b1-7bda-4c7b-8d85-dcf7d16395a6';
    sunk_barrel CONSTANT uuid := '5e947427-6c27-4d54-afda-c6fa494470b6';
    copies CONSTANT uuid[] := ARRAY['86a9b700-74d1-4fe8-9433-15176dee059c', '8c9734a5-1137-4753-bfd2-8a9557b49956',
                                    'a40926e8-73cc-4040-a64b-f14c161d67cf', '02abb1dd-16fc-42f8-8161-7c56cb1353a6']::uuid[];
    summer16 CONSTANT text := '/tilesets/mana-seed/summer-forest/summer sheets/summer 16x16.png';
    autumn16 CONSTANT text := '/tilesets/mana-seed/autumn-forest/autumn sheets/autumn 16x16.png';
    clean16 CONSTANT text := '/tilesets/mana-seed/winter-forest/winter sheets/winter (clean) 16x16.png';
    snowy16 CONSTANT text := '/tilesets/mana-seed/winter-forest/winter sheets/winter (snowy) 16x16.png';
    summer32 CONSTANT text := '/tilesets/mana-seed/summer-forest/summer sheets/summer 32x32.png';
    autumn32 CONSTANT text := '/tilesets/mana-seed/autumn-forest/autumn sheets/autumn 32x32.png';
    clean32 CONSTANT text := '/tilesets/mana-seed/winter-forest/winter sheets/winter (clean) 32x32.png';
    leaves32 CONSTANT text := '/tilesets/mana-seed/winter-forest/winter sheets/winter (leaves) 32x32.png';
    snowytop32 CONSTANT text := '/tilesets/mana-seed/winter-forest/winter sheets/winter (snowy top) 32x32.png';
    snowy32 CONSTANT text := '/tilesets/mana-seed/winter-forest/winter sheets/winter (snowy) 32x32.png';
    accessories16 CONSTANT text := '/tilesets/mana-seed/village-accessories/village accessories 16x16.png';
    good integer;
BEGIN
    -- A database with none of the pieces has no catalog (the up did nothing).
    IF NOT EXISTS (SELECT 1 FROM asset WHERE id IN (cattails, water_plant, lily_pad, light_lily_pad, small_lily_pad,
                                                     tiny_lily_pad, water_vine, sunk_barrel)) THEN
        RETURN;
    END IF;

    UPDATE village_object SET asset_id = cattails
     WHERE asset_id IN ('86a9b700-74d1-4fe8-9433-15176dee059c', '8c9734a5-1137-4753-bfd2-8a9557b49956');
    UPDATE village_object SET asset_id = water_plant
     WHERE asset_id IN ('a40926e8-73cc-4040-a64b-f14c161d67cf', '02abb1dd-16fc-42f8-8161-7c56cb1353a6');

    DELETE FROM asset_state_tag WHERE state_id IN (SELECT id FROM asset_state WHERE asset_id = ANY (copies));
    DELETE FROM asset_state WHERE asset_id = ANY (copies);
    DELETE FROM asset_refresh_default WHERE asset_id = ANY (copies);
    DELETE FROM asset WHERE id = ANY (copies);

    -- The Mana Seed rects, as they were before LLM-967h.
    CREATE TEMP TABLE llm967h_down (asset_id uuid NOT NULL, state text NOT NULL, sheet text NOT NULL,
                                    src_x integer NOT NULL, src_y integer NOT NULL, size integer NOT NULL,
                                    PRIMARY KEY (asset_id, state)) ON COMMIT DROP;
    INSERT INTO llm967h_down
    SELECT p.asset_id, s.state, s.sheet, p.src_x, p.src_y, 16
      FROM (VALUES (cattails, 64, 32), (water_plant, 80, 32), (light_lily_pad, 16, 32), (small_lily_pad, 0, 32),
                   (tiny_lily_pad, 48, 32), (water_vine, 32, 32)) p(asset_id, src_x, src_y)
     CROSS JOIN (VALUES ('default', summer16), ('autumn', autumn16), ('winter-clean', clean16),
                        ('winter-snowy', snowy16)) s(state, sheet);
    INSERT INTO llm967h_down
    SELECT lily_pad, s.state, s.sheet, 192, 0, 32
      FROM (VALUES ('default', summer32), ('autumn', autumn32), ('winter-clean', clean32), ('winter-leaves', leaves32),
                   ('winter-snowy-top', snowytop32), ('winter-snowy', snowy32)) s(state, sheet);
    INSERT INTO llm967h_down VALUES (sunk_barrel, 'default', accessories16, 160, 32, 16);

    UPDATE asset_state s
       SET sheet = d.sheet, src_x = d.src_x, src_y = d.src_y, src_w = d.size, src_h = d.size,
           frame_count = 1, frame_rate = 0
      FROM llm967h_down d
     WHERE s.asset_id = d.asset_id AND s.state = d.state;

    -- Every piece that exists is back on Mana Seed, and no copy is left.
    SELECT count(*) INTO good
      FROM asset_state s JOIN llm967h_down d ON d.asset_id = s.asset_id AND d.state = s.state
     WHERE s.sheet = d.sheet AND s.src_x = d.src_x AND s.src_y = d.src_y AND s.src_w = d.size
       AND s.src_h = d.size AND s.frame_count = 1 AND s.frame_rate = 0;
    IF good <> (SELECT count(*) FROM llm967h_down d JOIN asset_state s ON s.asset_id = d.asset_id AND s.state = d.state) THEN
        RAISE EXCEPTION 'LLM-967h down: only % water-edge states are back on Mana Seed', good;
    END IF;
    IF EXISTS (SELECT 1 FROM asset WHERE id = ANY (copies))
       OR EXISTS (SELECT 1 FROM village_object WHERE asset_id = ANY (copies)) THEN
        RAISE EXCEPTION 'LLM-967h down: a clump copy is left behind';
    END IF;
END $$;

COMMIT;
