-- ZBBS-HOME-254 — Bring farmer + dairykeeper into the worker scheduler
-- pool and finish wiring the Market Stall (Fancy) asset.
--
-- Two long-standing config gaps observed when trying to get Moses
-- James and Elizabeth Ellis to walk home at shift end after
-- HOME-251 / HOME-253 deployed:
--
-- 1. The `farmer` and `dairykeeper` attribute definitions had empty
--    `behaviors` arrays, so the worker scheduler's
--    `behaviors @> [{"type":"worker"}]` filter never matched them.
--    The scheduler ignored both keepers entirely, so the new
--    mechanical-walk path (HOME-253) couldn't dispatch their
--    shift transitions. Marking both as workers fixes the
--    enrollment.
--
-- 2. The Market Stall (Fancy) asset had its `open` and `closed`
--    asset_state rows but no `asset_state_tag` rows wiring them to
--    the `occupied` / `unoccupied` tags that
--    refreshStructureOccupancyState looks up. Ellis Farm therefore
--    stayed visually "open" when Elizabeth walked off shift even
--    though James Farm (Tiled stall, tags present) flipped closed
--    correctly. Same shape as the stand_offset / visible_when_inside
--    gaps HOME-248 patched on this asset.
--
-- Both UPDATEs were applied directly to the production zbbs DB so
-- Moses + Elizabeth could be observed tonight; this migration brings
-- the source of truth in line.

BEGIN;

UPDATE attribute_definition
   SET behaviors = '[{"type": "worker"}]'::jsonb
 WHERE slug IN ('farmer', 'dairykeeper')
   AND behaviors = '[]'::jsonb;

INSERT INTO asset_state_tag (state_id, tag)
SELECT s.id, 'occupied'
  FROM asset_state s JOIN asset a ON a.id = s.asset_id
 WHERE a.name = 'Market Stall (Fancy)' AND s.state = 'open'
   AND NOT EXISTS (
     SELECT 1 FROM asset_state_tag t WHERE t.state_id = s.id AND t.tag = 'occupied'
   );

INSERT INTO asset_state_tag (state_id, tag)
SELECT s.id, 'unoccupied'
  FROM asset_state s JOIN asset a ON a.id = s.asset_id
 WHERE a.name = 'Market Stall (Fancy)' AND s.state = 'closed'
   AND NOT EXISTS (
     SELECT 1 FROM asset_state_tag t WHERE t.state_id = s.id AND t.tag = 'unoccupied'
   );

COMMIT;
