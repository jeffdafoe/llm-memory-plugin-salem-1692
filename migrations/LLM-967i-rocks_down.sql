-- LLM-967i down: the Mana Seed rocks again. Every rock state returns to its
-- Mana Seed forest rect, one still frame each. No state was added or removed
-- by the up, so none is removed here, and no tag or placement changed.

BEGIN;

DO $$
DECLARE
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
    bad integer;
BEGIN
    -- The Mana Seed rects, as they were before LLM-967i.
    CREATE TEMP TABLE llm967i_down (asset_id uuid NOT NULL, state text NOT NULL, sheet text NOT NULL,
                                    src_x integer NOT NULL, src_y integer NOT NULL, size integer NOT NULL,
                                    PRIMARY KEY (asset_id, state)) ON COMMIT DROP;
    INSERT INTO llm967i_down
    SELECT p.asset_id::uuid, s.state, s.sheet, p.src_x, 48, 16
      FROM (VALUES ('055d4cb0-99d1-44b0-bbe2-e968dc868cc2', 0),     -- Pebble
                   ('24ca2da0-4dd4-474a-a471-7ce818742818', 16),    -- Tiny Rock
                   ('adb0dbe7-5406-479c-a5c7-54a30fd958de', 32),    -- Medium Rocks
                   ('f12a835e-74bd-44eb-9788-c9627e896e4f', 48),    -- Large Rocks
                   ('3552f47f-1ebb-420c-8871-3578f1ee501a', 64),    -- Boulder Pair
                   ('004a194c-b409-45ec-9d89-595d10372008', 80))    -- Flat Rock
           p(asset_id, src_x)
     CROSS JOIN (VALUES ('default', summer16), ('autumn', autumn16), ('winter-clean', clean16),
                        ('winter-snowy', snowy16)) s(state, sheet);
    INSERT INTO llm967i_down
    SELECT p.asset_id::uuid, s.state, s.sheet, p.src_x, 0, 32
      FROM (VALUES ('dcec1fc9-e629-4649-974c-3c298cd7e39b', 64),    -- Small Rock
                   ('87e8703b-2be9-4212-a06a-3372d7f9dfcb', 96))    -- River Rock
           p(asset_id, src_x)
     CROSS JOIN (VALUES ('default', summer32), ('autumn', autumn32), ('winter-clean', clean32), ('winter-leaves', leaves32),
                        ('winter-snowy-top', snowytop32), ('winter-snowy', snowy32)) s(state, sheet);
    -- A rock that is not in this database takes no part (a fresh database has
    -- no catalog; then this does nothing).
    DELETE FROM llm967i_down d WHERE NOT EXISTS (SELECT 1 FROM asset WHERE id = d.asset_id);

    UPDATE asset_state s
       SET sheet = d.sheet, src_x = d.src_x, src_y = d.src_y, src_w = d.size, src_h = d.size,
           frame_count = 1, frame_rate = 0
      FROM llm967i_down d
     WHERE s.asset_id = d.asset_id AND s.state = d.state;

    -- Every rock that exists has exactly its Mana Seed states again: no state
    -- missing, none extra, every value as before LLM-967i.
    SELECT count(*) INTO bad FROM (
        (SELECT asset_id, state, sheet, src_x, src_y, size AS src_w, size AS src_h, 1 AS frame_count,
                0::double precision AS frame_rate
           FROM llm967i_down
         EXCEPT
         SELECT s.asset_id, s.state, s.sheet, s.src_x, s.src_y, s.src_w, s.src_h, s.frame_count, s.frame_rate
           FROM asset_state s WHERE s.asset_id IN (SELECT asset_id FROM llm967i_down))
        UNION ALL
        (SELECT s.asset_id, s.state, s.sheet, s.src_x, s.src_y, s.src_w, s.src_h, s.frame_count, s.frame_rate
           FROM asset_state s WHERE s.asset_id IN (SELECT asset_id FROM llm967i_down)
         EXCEPT
         SELECT asset_id, state, sheet, src_x, src_y, size, size, 1, 0::double precision
           FROM llm967i_down)
    ) diff;
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-967i down: % rock state rows differ from the Mana Seed rects', bad;
    END IF;
END $$;

COMMIT;
