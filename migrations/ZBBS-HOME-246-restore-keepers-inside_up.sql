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
-- Filter: actor with work_structure_id set, currently inside=false
-- and NULL inside_structure_id, AND physically within ~5 tiles of
-- their work_structure anchor + loiter offset. Conservative — only
-- restores when they're clearly meant to be there. Doesn't disturb
-- actors who legitimately walked elsewhere.

BEGIN;

UPDATE actor a
   SET inside_structure_id = a.work_structure_id,
       inside = TRUE
  FROM village_object vo
 WHERE a.work_structure_id IS NOT NULL
   AND a.inside_structure_id IS NULL
   AND vo.id = a.work_structure_id
   AND ((a.current_x - (vo.x + COALESCE(vo.loiter_offset_x, 0) * 32))
      * (a.current_x - (vo.x + COALESCE(vo.loiter_offset_x, 0) * 32))
      + (a.current_y - (vo.y + COALESCE(vo.loiter_offset_y, 0) * 32))
      * (a.current_y - (vo.y + COALESCE(vo.loiter_offset_y, 0) * 32))) < 25600;
-- 25600 = 160px = 5 tiles squared.

COMMIT;
