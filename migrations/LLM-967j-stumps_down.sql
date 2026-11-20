-- LLM-967j down: the Mana Seed stumps and logs again. Every state returns to
-- its Mana Seed rect, one still frame each. No state was added or removed by
-- the up, so none is removed here, and no tag or placement changed.

BEGIN;

DO $$
DECLARE
    summer48 CONSTANT text := '/tilesets/mana-seed/summer-forest/summer sheets/summer 48x32.png';
    autumn48 CONSTANT text := '/tilesets/mana-seed/autumn-forest/autumn sheets/autumn 48x32.png';
    clean48 CONSTANT text := '/tilesets/mana-seed/winter-forest/winter sheets/winter (clean) 48x32.png';
    leaves48 CONSTANT text := '/tilesets/mana-seed/winter-forest/winter sheets/winter (leaves) 48x32.png';
    snowytop48 CONSTANT text := '/tilesets/mana-seed/winter-forest/winter sheets/winter (snowy top) 48x32.png';
    snowy48 CONSTANT text := '/tilesets/mana-seed/winter-forest/winter sheets/winter (snowy) 48x32.png';
    summer32 CONSTANT text := '/tilesets/mana-seed/summer-forest/summer sheets/summer 32x32.png';
    autumn32 CONSTANT text := '/tilesets/mana-seed/autumn-forest/autumn sheets/autumn 32x32.png';
    clean32 CONSTANT text := '/tilesets/mana-seed/winter-forest/winter sheets/winter (clean) 32x32.png';
    leaves32 CONSTANT text := '/tilesets/mana-seed/winter-forest/winter sheets/winter (leaves) 32x32.png';
    snowytop32 CONSTANT text := '/tilesets/mana-seed/winter-forest/winter sheets/winter (snowy top) 32x32.png';
    snowy32 CONSTANT text := '/tilesets/mana-seed/winter-forest/winter sheets/winter (snowy) 32x32.png';
    accessories32 CONSTANT text := '/tilesets/mana-seed/village-accessories/village accessories 32x32.png';
    camp16 CONSTANT text := '/tilesets/mana-seed/travelers-camp/travelers camp tent 16x16.png';
    bad integer;
BEGIN
    -- The Mana Seed rects, as they were before LLM-967j.
    CREATE TEMP TABLE llm967j_down (asset_id uuid NOT NULL, state text NOT NULL, sheet text NOT NULL,
                                    src_x integer NOT NULL, src_w integer NOT NULL, src_h integer NOT NULL,
                                    PRIMARY KEY (asset_id, state)) ON COMMIT DROP;
    INSERT INTO llm967j_down
    SELECT p.asset_id::uuid, s.state, s.sheet, p.src_x, 48, 32
      FROM (VALUES ('c1120ffa-8bef-46e3-b95b-626082a13dab', 0),     -- Big Stump
                   ('1d0accaa-dc6b-4ab1-8d3d-ad3076b5efd5', 48),    -- Fallen Log
                   ('6e894192-421d-45a0-b67c-d34d8f0f5885', 96))    -- Fallen Tree 2
           p(asset_id, src_x)
     CROSS JOIN (VALUES ('default', summer48), ('autumn', autumn48), ('winter-clean', clean48), ('winter-leaves', leaves48),
                        ('winter-snowy-top', snowytop48), ('winter-snowy', snowy48)) s(state, sheet);
    INSERT INTO llm967j_down
    SELECT p.asset_id::uuid, s.state, s.sheet, p.src_x, 32, 32
      FROM (VALUES ('f363320f-a5f0-43d2-87f5-47f2a8a370fe', 128),   -- Tree Stump
                   ('cd7f06e8-3536-4e72-bae9-24e79230c925', 160))   -- Fallen Tree
           p(asset_id, src_x)
     CROSS JOIN (VALUES ('default', summer32), ('autumn', autumn32), ('winter-clean', clean32), ('winter-leaves', leaves32),
                        ('winter-snowy-top', snowytop32), ('winter-snowy', snowy32)) s(state, sheet);
    INSERT INTO llm967j_down VALUES
        ('1e7905d7-3f3f-490b-8065-ed1924834415', 'default', accessories32, 0, 32, 32),   -- Small Stump
        ('03cbbdab-c50d-432b-a3f7-9e642f3de290', 'default', accessories32, 32, 32, 32),  -- Stump (Axe)
        ('2c2a423d-afea-45e6-a8bb-f26e0ef80e1e', 'default', camp16, 0, 16, 16);          -- Camp Stump
    -- A piece that is not in this database takes no part (a fresh database
    -- has no catalog; then this does nothing).
    DELETE FROM llm967j_down d WHERE NOT EXISTS (SELECT 1 FROM asset WHERE id = d.asset_id);

    UPDATE asset_state s
       SET sheet = d.sheet, src_x = d.src_x, src_y = 0, src_w = d.src_w, src_h = d.src_h,
           frame_count = 1, frame_rate = 0
      FROM llm967j_down d
     WHERE s.asset_id = d.asset_id AND s.state = d.state;

    -- Every piece that exists has exactly its Mana Seed states again: no state
    -- missing, none extra, every value as before LLM-967j.
    SELECT count(*) INTO bad FROM (
        (SELECT asset_id, state, sheet, src_x, 0 AS src_y, src_w, src_h, 1 AS frame_count,
                0::double precision AS frame_rate
           FROM llm967j_down
         EXCEPT
         SELECT s.asset_id, s.state, s.sheet, s.src_x, s.src_y, s.src_w, s.src_h, s.frame_count, s.frame_rate
           FROM asset_state s WHERE s.asset_id IN (SELECT asset_id FROM llm967j_down))
        UNION ALL
        (SELECT s.asset_id, s.state, s.sheet, s.src_x, s.src_y, s.src_w, s.src_h, s.frame_count, s.frame_rate
           FROM asset_state s WHERE s.asset_id IN (SELECT asset_id FROM llm967j_down)
         EXCEPT
         SELECT asset_id, state, sheet, src_x, 0, src_w, src_h, 1, 0::double precision
           FROM llm967j_down)
    ) diff;
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-967j down: % stump and log state rows differ from the Mana Seed rects', bad;
    END IF;
END $$;

COMMIT;
