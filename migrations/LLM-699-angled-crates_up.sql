-- LLM-699: every crate in the angled (corner-on) view of the buildings.
--
-- "Crate (Dark)" and "Crate (Light)" (LLM-690) were cut from Mana Seed
-- "village accessories 16x32.png", which draws its crates straight-on: the lid
-- from above with the front face under it. The village buildings are drawn at
-- an angle, so those crates looked wrong beside them. Only "Crate" (32x32 cell
-- (0,1)) is angled — and its lid-loose state was the light straight-on open
-- crate (32x32 cell (1,1)), so a broken dark crate turned light and straight.
-- The pack has no other angled crate and no angled open crate.
--
-- The new sheet angled-crates.png is one strip of four 40x40 frames, original
-- art made from the pack's angled crate (generator: llm-memory-village-tiles
-- tools/angled-crates/main.go):
--   x   0  dark               the pack's angled crate
--   x  40  dark, lid loose    the lid slid back off the open box
--   x  80  light              the same crate in the light crates' colours
--   x 120  light, lid loose
-- "Crate" and "Crate (Dark)" take the dark pair, "Crate (Light)" the light pair.
--
-- The crate sits at (4,8) in each 40x40 frame. Its ground point, (16, 27.2) in
-- the old 32x32 cell at anchor 0.5/0.85, is (20, 35.2) in the new frame, so
-- anchor_y becomes 0.88 and every placed crate stands where it stood.
--
-- Only asset / asset_state columns change: REFERENCE data, never
-- checkpoint-clobbered, so this needs no engine stop. Ids, placements and the
-- minor-work tags stay. The catalog is boot-loaded; the deploy restarts the
-- engine.
--
-- THIS MIGRATION ALONE DOES NOT DRAW THE CRATES. The sheet travels by scp to
-- /var/www/llm-memory-salem-1692/tilesets/mana-seed/village-accessories/
-- (owner www-data).
--
-- "Crate" was seeded by hand, so a schema-only database has none of these
-- rows; every statement and check is scoped to the assets that exist.

BEGIN;

CREATE TEMP TABLE llm699_frame (asset_id uuid, state text, src_x int) ON COMMIT DROP;
INSERT INTO llm699_frame VALUES
    ('7bf7022c-c354-4333-a607-bb42650d2666', 'default',     0),   -- Crate
    ('7bf7022c-c354-4333-a607-bb42650d2666', 'lid-loose',  40),
    ('019e5f00-c401-7a10-9e00-000000690001', 'default',     0),   -- Crate (Dark)
    ('019e5f00-c401-7a10-9e00-000000690001', 'lid-loose',  40),
    ('019e5f00-c401-7a10-9e00-000000690002', 'default',    80),   -- Crate (Light)
    ('019e5f00-c401-7a10-9e00-000000690002', 'lid-loose', 120);

UPDATE asset_state st
   SET sheet = '/tilesets/mana-seed/village-accessories/angled-crates.png',
       src_x = f.src_x, src_y = 0, src_w = 40, src_h = 40
  FROM llm699_frame f
 WHERE st.asset_id = f.asset_id AND st.state = f.state;

UPDATE asset
   SET anchor_y = 0.88,
       source_file = 'composite: angled-crates.png (village accessories 32x32.png (0,1))'
 WHERE id IN (SELECT DISTINCT asset_id FROM llm699_frame);

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM llm699_frame f
                WHERE EXISTS (SELECT 1 FROM asset a WHERE a.id = f.asset_id)
                  AND NOT EXISTS (SELECT 1 FROM asset_state st
                                   WHERE st.asset_id = f.asset_id AND st.state = f.state
                                     AND st.sheet = '/tilesets/mana-seed/village-accessories/angled-crates.png'
                                     AND st.src_x = f.src_x AND st.src_y = 0
                                     AND st.src_w = 40 AND st.src_h = 40)) THEN
        RAISE EXCEPTION 'LLM-699: a crate state is missing or not on its angled-crates frame';
    END IF;
    IF EXISTS (SELECT 1 FROM asset
                WHERE id IN (SELECT asset_id FROM llm699_frame)
                  AND (anchor_x IS DISTINCT FROM 0.5 OR anchor_y IS DISTINCT FROM 0.88)) THEN
        RAISE EXCEPTION 'LLM-699: a crate asset is not anchored at 0.5/0.88';
    END IF;
END $$;

COMMIT;
