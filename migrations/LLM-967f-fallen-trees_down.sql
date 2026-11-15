-- LLM-967f down: the storm damage back on the Mana Seed pack art. Each top
-- asset's anchor, footprint and frames, the Storm Stump's frames and anchor,
-- and the Fallen Trunk's season sheets return to their LLM-678 / LLM-789 /
-- LLM-849 values (as live before LLM-967f). Run this BEFORE reverting the
-- engine: its road variants carry the stump offsets of the art in use.

BEGIN;

DO $$
DECLARE
    stump CONSTANT uuid := '019e5f00-c401-7a10-9e00-000000678004';
    trunk CONSTANT uuid := '019e5f00-c401-7a10-9e00-000000678005';
    snapped CONSTANT text := '/tilesets/mana-seed/growable-trees/public-works-fallen-trees-snapped.png';
    flat CONSTANT text := '/tilesets/mana-seed/growable-trees/public-works-fallen-trees.png';
BEGIN
    IF NOT EXISTS (SELECT 1 FROM asset WHERE id = '019e5f00-c401-7a10-9e00-000000678001') THEN
        RETURN;
    END IF;

    CREATE TEMP TABLE llm967f_old_top (
        asset_id uuid PRIMARY KEY, anchor_x double precision NOT NULL, anchor_y double precision NOT NULL,
        fl integer NOT NULL, fr integer NOT NULL, ft integer NOT NULL, fb integer NOT NULL,
        bare_x integer NOT NULL, winter_x integer NOT NULL, y integer NOT NULL, w integer NOT NULL, h integer NOT NULL) ON COMMIT DROP;
    INSERT INTO llm967f_old_top VALUES
        ('019e5f00-c401-7a10-9e00-000000678001', 0.5797101449275363, 0.5555555555555556, 1, 1, 1, 1,   0,  71,   0, 69, 72),
        ('019e5f00-c401-7a10-9e00-000000789001', 0.5714285714285714, 0.42857142857142855, 1, 1, 1, 1, 142, 214,   0, 70, 56),
        ('019e5f00-c401-7a10-9e00-000000678002', 0.5194805194805194, 0.5063291139240507, 2, 2, 2, 2,   0,  79,  74, 77, 79),
        ('019e5f00-c401-7a10-9e00-000000789002', 0.49382716049382713, 0.4878048780487805, 2, 2, 1, 2, 158, 241,  74, 81, 82),
        ('019e5f00-c401-7a10-9e00-000000678003', 0.5063291139240507, 0.4528301886792453, 1, 2, 1, 1,   0,  81, 158, 79, 53),
        ('019e5f00-c401-7a10-9e00-000000789003', 0.5063291139240507, 0.4528301886792453, 1, 2, 1, 1, 162, 243, 158, 79, 53);

    UPDATE asset a
       SET anchor_x = t.anchor_x, anchor_y = t.anchor_y,
           footprint_left = t.fl, footprint_right = t.fr, footprint_top = t.ft, footprint_bottom = t.fb
      FROM llm967f_old_top t
     WHERE a.id = t.asset_id;
    UPDATE asset_state s
       SET sheet = snapped, src_x = CASE s.state WHEN 'bare' THEN t.bare_x ELSE t.winter_x END,
           src_y = t.y, src_w = t.w, src_h = t.h, frame_count = 1, frame_rate = 0
      FROM llm967f_old_top t
     WHERE s.asset_id = t.asset_id AND s.state IN ('bare', 'winter');

    UPDATE asset SET anchor_x = 0.4861111111111111, anchor_y = 0.890625 WHERE id = stump;
    UPDATE asset_state s
       SET sheet = flat, src_x = o.x, src_y = 66, src_w = 36, src_h = 32, frame_count = 1, frame_rate = 0
      FROM (VALUES ('maple-bare', 0), ('maple-winter', 36), ('chestnut-bare', 72), ('chestnut-winter', 108),
                   ('birch-bare', 144), ('birch-winter', 180)) o(state, x)
     WHERE s.asset_id = stump AND s.state = o.state;

    UPDATE asset_state s
       SET sheet = o.sheet, src_x = 160, src_y = 0, src_w = 32, src_h = 32, frame_count = 1, frame_rate = 0
      FROM (VALUES ('default', '/tilesets/mana-seed/summer-forest/summer sheets/summer 32x32.png'),
                   ('autumn', '/tilesets/mana-seed/autumn-forest/autumn sheets/autumn 32x32.png'),
                   ('winter-clean', '/tilesets/mana-seed/winter-forest/winter sheets/winter (clean) 32x32.png'),
                   ('winter-leaves', '/tilesets/mana-seed/winter-forest/winter sheets/winter (leaves) 32x32.png'),
                   ('winter-snowy', '/tilesets/mana-seed/winter-forest/winter sheets/winter (snowy) 32x32.png'),
                   ('winter-snowy-top', '/tilesets/mana-seed/winter-forest/winter sheets/winter (snowy top) 32x32.png')) o(state, sheet)
     WHERE s.asset_id = trunk AND s.state = o.state;

    IF EXISTS (SELECT 1 FROM asset_state
                WHERE asset_id IN (SELECT asset_id FROM llm967f_old_top UNION ALL SELECT stump UNION ALL SELECT trunk)
                  AND sheet LIKE 'res://%') THEN
        RAISE EXCEPTION 'LLM-967f down: a fallen-tree state is still on the painted sheets';
    END IF;
END $$;

COMMIT;
