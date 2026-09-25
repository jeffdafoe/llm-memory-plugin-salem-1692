-- LLM-675 follow-up down: point the Debris states back at the first 32x32
-- sheet (public-works-debris.png: worn x 0, storm x 32) and drop the debris
-- slots, so the overlay draws at each building's anchor again. Placed overlays
-- are untouched; they redraw with the small art on the next restart.

BEGIN;

-- Only the seven slots the up owns; a debris slot any other asset gains later
-- is not this migration's to remove.
DELETE FROM asset_slot
 WHERE slot_name = 'debris'
   AND asset_id IN ('b24c6666-0830-481d-b502-be827f0c36c2', '50d047fe-6e0e-4969-85a1-ea8158a3f2d8',
                    'b967fd3a-05ca-46e1-a84b-ea8226b72b6b', 'f02d52a7-ac65-4db6-a52a-fec5cdd6385e',
                    '5073eb02-6f3f-4191-aced-242d8502d153', '80c83812-f661-4f1d-8b70-99a5449814b2',
                    'ab66597b-cf67-4a7a-a48f-d0fbc36117a4');

UPDATE asset SET fits_slot = NULL
 WHERE id = '019e5f00-c401-7a10-9e00-000000675001';

UPDATE asset_state
   SET sheet = '/tilesets/mana-seed/village-accessories/public-works-debris.png',
       src_x = CASE state WHEN 'worn' THEN 0 ELSE 32 END,
       src_y = 0, src_w = 32, src_h = 32
 WHERE asset_id = '019e5f00-c401-7a10-9e00-000000675001'
   AND state IN ('worn', 'storm');

COMMIT;
