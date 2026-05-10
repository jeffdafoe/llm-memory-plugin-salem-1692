-- ZBBS-HOME-246 — One-shot restore of keepers stuck outside their
-- work_structure post HOME-244 deploy. After running buy_walker
-- trips, John (and possibly Josiah / Prudence) ended up at the
-- door of their own structure with inside_structure_id=NULL,
-- because the return-walk arrival pipeline didn't flip `inside`
-- back to true.
--
-- HOME-245 fixed cancelBuyTrip to restore the flag on subsequent
-- inbound arrivals. This migration backfills the current state for
-- any keeper who's already stuck outside.
--
-- Filter: actor with work_structure_id set, currently NULL
-- inside_structure_id, AND physically within their work_structure
-- asset's footprint (footprint_left/right/top/bottom in tiles ×
-- 32px). Footprint-based avoids the bad case where the actor is at
-- the visitor loiter slot OUTSIDE the building — flipping the flag
-- there would render them inside while visually outside.

BEGIN;

UPDATE actor a
   SET inside_structure_id = a.work_structure_id,
       inside = TRUE
  FROM village_object vo
  JOIN asset s ON s.id = vo.asset_id
 WHERE a.work_structure_id IS NOT NULL
   AND a.inside_structure_id IS NULL
   AND vo.id = a.work_structure_id
   AND a.current_x BETWEEN vo.x - s.footprint_left * 32 AND vo.x + s.footprint_right * 32
   AND a.current_y BETWEEN vo.y - s.footprint_top  * 32 AND vo.y + s.footprint_bottom * 32;

COMMIT;
