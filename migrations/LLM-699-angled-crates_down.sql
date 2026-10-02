-- LLM-699 down: the crates back on their Mana Seed frames at anchor 0.85.
-- "Crate": 32x32 sheet, default (0,32), lid-loose (32,32), no source_file.
-- "Crate (Dark)" / "Crate (Light)": 16x32 sheet, row 1 — dark 96 / 80,
-- light 160 / 192. Placements and tags are untouched.

BEGIN;

CREATE TEMP TABLE llm699_frame (asset_id uuid, state text, sheet text,
                                src_x int, src_y int, src_w int, src_h int) ON COMMIT DROP;
INSERT INTO llm699_frame VALUES
    ('7bf7022c-c354-4333-a607-bb42650d2666', 'default',
     '/tilesets/mana-seed/village-accessories/village accessories 32x32.png',   0, 32, 32, 32),
    ('7bf7022c-c354-4333-a607-bb42650d2666', 'lid-loose',
     '/tilesets/mana-seed/village-accessories/village accessories 32x32.png',  32, 32, 32, 32),
    ('019e5f00-c401-7a10-9e00-000000690001', 'default',
     '/tilesets/mana-seed/village-accessories/village accessories 16x32.png',  96, 32, 16, 32),
    ('019e5f00-c401-7a10-9e00-000000690001', 'lid-loose',
     '/tilesets/mana-seed/village-accessories/village accessories 16x32.png',  80, 32, 16, 32),
    ('019e5f00-c401-7a10-9e00-000000690002', 'default',
     '/tilesets/mana-seed/village-accessories/village accessories 16x32.png', 160, 32, 16, 32),
    ('019e5f00-c401-7a10-9e00-000000690002', 'lid-loose',
     '/tilesets/mana-seed/village-accessories/village accessories 16x32.png', 192, 32, 16, 32);

UPDATE asset_state st
   SET sheet = f.sheet, src_x = f.src_x, src_y = f.src_y, src_w = f.src_w, src_h = f.src_h
  FROM llm699_frame f
 WHERE st.asset_id = f.asset_id AND st.state = f.state;

UPDATE asset SET anchor_y = 0.85, source_file = NULL
 WHERE id = '7bf7022c-c354-4333-a607-bb42650d2666';

UPDATE asset SET anchor_y = 0.85, source_file = 'village accessories 16x32.png'
 WHERE id IN ('019e5f00-c401-7a10-9e00-000000690001', '019e5f00-c401-7a10-9e00-000000690002');

COMMIT;
