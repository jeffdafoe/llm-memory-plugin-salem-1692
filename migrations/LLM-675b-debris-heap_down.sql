-- LLM-675 follow-up down: point the Debris states back at the first 32x32
-- sheet (public-works-debris.png: worn x 0, storm x 32) and drop the debris
-- slots, so the overlay draws at each building's anchor again. Placed overlays
-- are untouched; they redraw with the small art on the next restart.

BEGIN;

DELETE FROM asset_slot WHERE slot_name = 'debris';

UPDATE asset SET fits_slot = NULL
 WHERE id = '019e5f00-c401-7a10-9e00-000000675001';

UPDATE asset_state
   SET sheet = '/tilesets/mana-seed/village-accessories/public-works-debris.png',
       src_x = CASE state WHEN 'worn' THEN 0 ELSE 32 END,
       src_y = 0, src_w = 32, src_h = 32
 WHERE asset_id = '019e5f00-c401-7a10-9e00-000000675001'
   AND state IN ('worn', 'storm');

COMMIT;
