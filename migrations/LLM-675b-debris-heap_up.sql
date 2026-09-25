-- LLM-675 follow-up: a bigger debris heap.
--
-- The first Debris art (LLM-675-shop-damage) was two 32x32 frames — at the
-- live render scale about a third of a building's width, and mostly hidden by
-- anyone standing at the shop's door pin, which is where an attached overlay
-- draws. Jeff saw it at Lewis's Workshop and asked for a bigger composite.
--
-- The new sheet public-works-debris-heap.png holds two 64x40 frames, composited
-- from the same licensed Mana Seed pieces plus a few more:
--   worn  (x 0)  — split boards leaning and fallen, with two dark plank piles
--                  ("summer 16x16.png" 5,0; "village accessories 16x32.png" 3,1)
--   storm (x 64) — the split log of "summer 48x32.png" (1,0) down across the
--                  door, with fallen branches (3,1 / 4,1), leaves (0,1 / 1,1)
--                  and a leaning board
-- A new file name, not an overwrite of the old sheet, so a client holding the
-- old texture cannot keep drawing it against the new frame size.
--
-- Only the two asset_state rows change: sheet, src_x, src_w, src_h. The asset,
-- its id and anchor, and every placed overlay stay as they are, so a damaged
-- shop redraws with the new heap on the next engine restart (the catalog is
-- boot-loaded) and client reload.
--
-- WHERE THE HEAP SITS. An attached overlay draws at the parent's slot named by
-- the overlay's fits_slot, else at the parent's anchor. The anchor (bottom
-- centre of the sprite) is right only where the door is centred; the business
-- building types differ, so each gets an authored "debris" slot (world px from
-- the anchor, the same units as the existing sign slots), placed against its
-- sprite:
--   Red House (Small)     0, 0    door centred above the anchor
--   Yellow Tower          0, 6    door centred; a touch lower, on the step
--   Black/Blue House (M) -60, 0   drawn at an angle — the door is on the left face
--   Market Stall x3       40, 24  the recorded "door" is behind the counter;
--                                 the heap goes on the ground at the stall's
--                                 front-right corner, clear of the counter where
--                                 customers stand
-- A building type with no debris slot falls back to the anchor.
--
-- THIS MIGRATION ALONE DOES NOT DRAW THE HEAP. The sheet travels by scp to
-- /var/www/llm-memory-salem-1692/tilesets/mana-seed/village-accessories/
-- (owner www-data). Its source is in llm-memory-village-tiles.
--
-- Rerun-safe and convergent: plain UPDATEs to fixed values; the slot rows are
-- upserted on (asset_id, slot_name). Exact-value validation at the end.

BEGIN;

UPDATE asset_state
   SET sheet = '/tilesets/mana-seed/village-accessories/public-works-debris-heap.png',
       src_x = CASE state WHEN 'worn' THEN 0 ELSE 64 END,
       src_y = 0, src_w = 64, src_h = 40
 WHERE asset_id = '019e5f00-c401-7a10-9e00-000000675001'
   AND state IN ('worn', 'storm');

UPDATE asset SET fits_slot = 'debris'
 WHERE id = '019e5f00-c401-7a10-9e00-000000675001';

-- The seven slots this migration owns — also what the validation and the
-- _down scope to.
CREATE TEMP TABLE llm675b_debris_slot (asset_id uuid PRIMARY KEY, offset_x int, offset_y int) ON COMMIT DROP;
INSERT INTO llm675b_debris_slot VALUES
    ('b24c6666-0830-481d-b502-be827f0c36c2',   0,  0),   -- Red House (Small)
    ('50d047fe-6e0e-4969-85a1-ea8158a3f2d8',   0,  6),   -- Yellow Tower
    ('b967fd3a-05ca-46e1-a84b-ea8226b72b6b', -60,  0),   -- Black House (Medium)
    ('f02d52a7-ac65-4db6-a52a-fec5cdd6385e', -60,  0),   -- Blue House (Medium)
    ('5073eb02-6f3f-4191-aced-242d8502d153',  40, 24),   -- Market Stall (Wood)
    ('80c83812-f661-4f1d-8b70-99a5449814b2',  40, 24),   -- Market Stall (Tiled)
    ('ab66597b-cf67-4a7a-a48f-d0fbc36117a4',  40, 24);   -- Market Stall (Fancy)

-- Upsert on the (asset_id, slot_name) unique key, so a rerun converges on
-- these offsets even if a slot drifted. Only building assets present in the
-- catalog get a row (a schema-only database has none).
INSERT INTO asset_slot (asset_id, slot_name, offset_x, offset_y)
SELECT d.asset_id, 'debris', d.offset_x, d.offset_y
  FROM llm675b_debris_slot d
 WHERE EXISTS (SELECT 1 FROM asset a WHERE a.id = d.asset_id)
ON CONFLICT (asset_id, slot_name) DO UPDATE
   SET offset_x = EXCLUDED.offset_x, offset_y = EXCLUDED.offset_y;

-- Validate the exact values, where the rows exist (a schema-only harness has
-- no catalog rows).
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM asset WHERE id = '019e5f00-c401-7a10-9e00-000000675001') THEN
        IF NOT EXISTS (SELECT 1 FROM asset
                        WHERE id = '019e5f00-c401-7a10-9e00-000000675001' AND fits_slot = 'debris') THEN
            RAISE EXCEPTION 'LLM-675 debris heap: the Debris asset does not fit the debris slot';
        END IF;
        IF (SELECT count(*) FROM asset_state
             WHERE asset_id = '019e5f00-c401-7a10-9e00-000000675001'
               AND sheet = '/tilesets/mana-seed/village-accessories/public-works-debris-heap.png'
               AND src_y = 0 AND src_w = 64 AND src_h = 40
               AND ((state = 'worn' AND src_x = 0) OR (state = 'storm' AND src_x = 64))) <> 2 THEN
            RAISE EXCEPTION 'LLM-675 debris heap: the worn/storm states are not exactly the 64x40 frames';
        END IF;
    END IF;
    IF EXISTS (SELECT 1 FROM llm675b_debris_slot d
                WHERE EXISTS (SELECT 1 FROM asset a WHERE a.id = d.asset_id)
                  AND NOT EXISTS (SELECT 1 FROM asset_slot s
                                   WHERE s.asset_id = d.asset_id AND s.slot_name = 'debris'
                                     AND s.offset_x = d.offset_x AND s.offset_y = d.offset_y)) THEN
        RAISE EXCEPTION 'LLM-675 debris heap: a business building type lacks its debris slot at the expected offset';
    END IF;
END $$;

COMMIT;
