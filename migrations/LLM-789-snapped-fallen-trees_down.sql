-- LLM-789 down: back to LLM-678's flat crown-west tops.
--
-- REFUSES while any of the three SW-pose assets is placed: the rolled-back
-- engine does not know them, so the tree would stay in the road for good. Clear
-- the road first — POST /api/village/umbilical/object/damage {id, action:
-- "repair"} — with the engine running, then re-run. (The stump such a tree
-- leaves is an ordinary Storm Stump and keeps its clock.)
--
-- A placed LLM-678-id top keeps its tile and is drawn flat again.

BEGIN;

DO $$
DECLARE placed int;
BEGIN
    SELECT count(*) INTO placed
      FROM village_object
     WHERE asset_id IN ('019e5f00-c401-7a10-9e00-000000789001', '019e5f00-c401-7a10-9e00-000000789002',
                        '019e5f00-c401-7a10-9e00-000000789003');
    IF placed > 0 THEN
        RAISE EXCEPTION
            'LLM-789 down: % SW-pose fallen tree(s) still in the village. Clear them first (engine running, umbilical), then re-run.',
            placed;
    END IF;
END $$;

DELETE FROM asset_state
 WHERE asset_id IN ('019e5f00-c401-7a10-9e00-000000789001', '019e5f00-c401-7a10-9e00-000000789002',
                    '019e5f00-c401-7a10-9e00-000000789003');
DELETE FROM asset
 WHERE id IN ('019e5f00-c401-7a10-9e00-000000789001', '019e5f00-c401-7a10-9e00-000000789002',
              '019e5f00-c401-7a10-9e00-000000789003');

UPDATE asset AS a
   SET name = v.name, anchor_x = v.anchor_x, anchor_y = v.anchor_y,
       footprint_left = v.fl, footprint_right = v.fr, footprint_top = v.ft, footprint_bottom = v.fb,
       source_file = v.source_file
  FROM (VALUES
    ('019e5f00-c401-7a10-9e00-000000678001', 'Fallen Maple',
     0.42857142857142855, 0.5333333333333333, 1, 1, 1, 1,
     'composite: growable tree (maple) no shadow.png, top col 4 rows 3/4, transposed'),
    ('019e5f00-c401-7a10-9e00-000000678002', 'Fallen Chestnut',
     0.49382716049382713, 0.36363636363636365, 2, 2, 1, 2,
     'composite: growable tree (chestnut) no shadow.png, top col 4 rows 3/4, transposed'),
    ('019e5f00-c401-7a10-9e00-000000678003', 'Fallen Birch',
     0.5555555555555556, 0.24242424242424243, 2, 1, 0, 1,
     'composite: growable tree (birch) no shadow.png, top col 4 rows 3/4, transposed')
  ) AS v(id, name, anchor_x, anchor_y, fl, fr, ft, fb, source_file)
 WHERE a.id = v.id::uuid;

UPDATE asset_state AS s
   SET sheet = '/tilesets/mana-seed/growable-trees/public-works-fallen-trees.png',
       src_x = v.src_x, src_y = v.src_y, src_w = v.src_w, src_h = v.src_h
  FROM (VALUES
    ('019e5f00-c401-7a10-9e00-000000678001', 'bare',     0, 0, 56, 45),
    ('019e5f00-c401-7a10-9e00-000000678001', 'winter',  81, 0, 56, 45),
    ('019e5f00-c401-7a10-9e00-000000678002', 'bare',   162, 0, 81, 66),
    ('019e5f00-c401-7a10-9e00-000000678002', 'winter', 243, 0, 81, 66),
    ('019e5f00-c401-7a10-9e00-000000678003', 'bare',   324, 0, 72, 33),
    ('019e5f00-c401-7a10-9e00-000000678003', 'winter', 405, 0, 72, 33)
  ) AS v(asset_id, state, src_x, src_y, src_w, src_h)
 WHERE s.asset_id = v.asset_id::uuid AND s.state = v.state;

COMMIT;
