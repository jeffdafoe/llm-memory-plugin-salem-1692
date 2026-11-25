-- LLM-968: painted market stalls, a new booth on new tiles: Market Stall
-- (Wood), (Tiled) and (Fancy), and the five stalls placed from them.
--
-- They drew Mana Seed village accessories 80x96.png, whose licence forbids
-- use alongside AI. tools/tree-art/stalls.py now paints them: client/assets/
-- trees/stall-wood.png, stall-tiled.png and stall-fancy.png, open then
-- closed, 128x224 frames shown at render_scale 1. Jeff chose a redesign
-- (2026-11-25): an open booth (a counter on its two front sides, four posts,
-- a gable roof, no wall behind the keeper) in period New England finishes,
-- with tiles that fit the art.
--
-- WHY THE TILES MOVE. The client draws a see-through structure's keeper
-- (visible_when_inside) on the stand tile BEFORE the stall, so the keeper
-- shows only through the art's empty pixels. The pack sheds showed them
-- through a window that had to line up with a stand tile, from placements
-- that sat anywhere in their anchor tile, so the keeper stood at a different
-- spot in each stall's art. The booth leaves the whole space between counter
-- and roof empty, and the placements move to their anchor tile's centre, so
-- every keeper stands at the same spot in the art: behind the counter, seen
-- from the waist up.
--
-- ASSETS (the three, live-only: no migration creates them):
--   render_scale 1; anchor 0.5 / (76/112): the keeper's feet, which are the
--   anchor tile's centre;
--   footprint left 1, right 1, top 1, bottom 0 (3 wide, 2 deep; was
--   5 x 4..6, wider than the art): every new footprint lies inside its old
--   one, so no tile becomes blocked;
--   door (0, -1): the back centre, on the footprint's north edge, so the walk
--   grid opens it north (pathfind.go doorExitSide) and the keeper comes in
--   from behind the counter; it is the footprint's one walkable tile;
--   stand (0, 0): the anchor tile, the middle of the booth;
--   states open (x 0) and closed (x 128), 128x224, one frame each.
-- Unchanged and checked: the tags (open occupied, closed unoccupied: the
-- stall shows open while someone is in it), visible_when_inside true, and
-- one debris slot at (40, 24) on each (world px from the anchor, so it holds
-- at any render_scale; the storm's debris lands in front of the booth's
-- right corner).
--
-- PLACEMENTS (every village_object on the three): x and y move to the centre
-- of the tile they are in (at most half a tile; the anchor tile, so every
-- tile-based rule, is unchanged); loiter offset (-1, 1), in front of the long
-- counter (the customers' spot; straight in front, (0, 1), a customer hides
-- the keeper). Checked against the live map with the engine's walk-grid
-- rules before writing this: at all five stalls the new door, the three
-- tiles north of it and the loiter tile are dry ground under no other
-- object's footprint.
--
-- A keeper inside a stall at deploy keeps InsideStructureID (the engine
-- reconciles it from the tile only when they move) and shows at the new
-- stand spot; their next walk starts from the old door tile, now open ground.
--
-- The counter close-ups (client/scripts/stall_scenes.gd, LLM-753, keyed by
-- the stall's kind) are LLM-969's repaint; nothing here touches them.
--
-- Each stall asset that exists is set, then checked both ways (states, tags)
-- or exactly (columns, slots, placements); any difference fails the
-- migration. A missing state is not recreated (it would come back without
-- its tag). ENGINE-OWNED data (the catalog, the placements), read at boot;
-- deploy.sh runs stop -> migrate -> start. Convergent: a rerun ends in the
-- same place and repairs a hand edit (the up writes the authored values over
-- whatever is there; it does not keep them to restore: the down restores the
-- five known placements by id).

BEGIN;

-- Hold off any other catalog or placement writer (the editor) between the
-- presence check and the checks below.
LOCK TABLE asset, asset_state, asset_state_tag, asset_slot, village_object IN SHARE ROW EXCLUSIVE MODE;

DO $$
DECLARE
    bad integer;
BEGIN
    CREATE TEMP TABLE llm968h_stall (asset_id uuid PRIMARY KEY, sheet text) ON COMMIT DROP;
    INSERT INTO llm968h_stall VALUES
        ('5073eb02-6f3f-4191-aced-242d8502d153', 'res://assets/trees/stall-wood.png'),    -- Market Stall (Wood)
        ('80c83812-f661-4f1d-8b70-99a5449814b2', 'res://assets/trees/stall-tiled.png'),   -- Market Stall (Tiled)
        ('ab66597b-cf67-4a7a-a48f-d0fbc36117a4', 'res://assets/trees/stall-fancy.png');   -- Market Stall (Fancy)
    DELETE FROM llm968h_stall v WHERE NOT EXISTS (SELECT 1 FROM asset WHERE id = v.asset_id);
    IF NOT EXISTS (SELECT 1 FROM llm968h_stall) THEN
        RETURN;
    END IF;

    CREATE TEMP TABLE llm968h_state ON COMMIT DROP AS
    SELECT v.asset_id, s.state, v.sheet, s.src_x, 0 AS src_y, 128 AS src_w, 224 AS src_h, 1 AS frame_count,
           0::double precision AS frame_rate, s.tag
      FROM llm968h_stall v
     CROSS JOIN (VALUES ('open', 0, 'occupied'), ('closed', 128, 'unoccupied')) s(state, src_x, tag);

    UPDATE asset a
       SET render_scale = 1, anchor_x = 0.5, anchor_y = 76.0::double precision / 112,
           footprint_left = 1, footprint_right = 1, footprint_top = 1, footprint_bottom = 0,
           door_offset_x = 0, door_offset_y = -1, stand_offset_x = 0, stand_offset_y = 0
      FROM llm968h_stall v
     WHERE a.id = v.asset_id;
    -- Existing states only: the check below fails on a missing one.
    UPDATE asset_state s
       SET sheet = v.sheet, src_x = v.src_x, src_y = v.src_y, src_w = v.src_w, src_h = v.src_h,
           frame_count = v.frame_count, frame_rate = v.frame_rate
      FROM llm968h_state v
     WHERE s.asset_id = v.asset_id AND s.state = v.state;
    -- The placements: to their tile's centre, customers front-left.
    UPDATE village_object o
       SET x = floor(o.x / 32) * 32 + 16, y = floor(o.y / 32) * 32 + 16,
           loiter_offset_x = -1, loiter_offset_y = 1
      FROM llm968h_stall v
     WHERE o.asset_id = v.asset_id;

    SELECT count(*) INTO bad FROM (
        (SELECT asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate FROM llm968h_state
         EXCEPT
         SELECT s.asset_id, s.state, s.sheet, s.src_x, s.src_y, s.src_w, s.src_h, s.frame_count, s.frame_rate
           FROM asset_state s JOIN llm968h_stall v ON v.asset_id = s.asset_id)
        UNION ALL
        (SELECT s.asset_id, s.state, s.sheet, s.src_x, s.src_y, s.src_w, s.src_h, s.frame_count, s.frame_rate
           FROM asset_state s JOIN llm968h_stall v ON v.asset_id = s.asset_id
         EXCEPT
         SELECT asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate FROM llm968h_state)
    ) diff;
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968h: % stall state rows differ from the authored values', bad;
    END IF;
    SELECT count(*) INTO bad FROM (
        (SELECT asset_id, state, tag FROM llm968h_state
         EXCEPT
         SELECT s.asset_id, s.state::text, t.tag::text
           FROM asset_state s JOIN llm968h_stall v ON v.asset_id = s.asset_id JOIN asset_state_tag t ON t.state_id = s.id)
        UNION ALL
        (SELECT s.asset_id, s.state::text, t.tag::text
           FROM asset_state s JOIN llm968h_stall v ON v.asset_id = s.asset_id JOIN asset_state_tag t ON t.state_id = s.id
         EXCEPT
         SELECT asset_id, state, tag FROM llm968h_state)
    ) diff;
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968h: % stall state tags differ from open occupied / closed unoccupied', bad;
    END IF;
    SELECT count(*) INTO bad FROM asset a JOIN llm968h_stall v ON v.asset_id = a.id
     WHERE (a.render_scale, a.anchor_x, a.anchor_y, a.footprint_left, a.footprint_right, a.footprint_top,
            a.footprint_bottom, a.door_offset_x, a.door_offset_y, a.stand_offset_x, a.stand_offset_y,
            a.visible_when_inside)
           IS DISTINCT FROM (1::double precision, 0.5::double precision, 76.0::double precision / 112, 1, 1, 1, 0,
                             0, -1, 0, 0, true);
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968h: % stall asset(s) differ from the authored columns', bad;
    END IF;
    SELECT count(*) INTO bad FROM llm968h_stall v
     WHERE (SELECT count(*) FROM asset_slot sl WHERE sl.asset_id = v.asset_id) <> 1
        OR NOT EXISTS (SELECT 1 FROM asset_slot sl WHERE sl.asset_id = v.asset_id AND sl.slot_name = 'debris'
                                                     AND sl.offset_x = 40 AND sl.offset_y = 24);
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968h: % stall asset(s) without exactly their debris slot at (40, 24)', bad;
    END IF;
    SELECT count(*) INTO bad FROM village_object o JOIN llm968h_stall v ON v.asset_id = o.asset_id
     WHERE o.x <> floor(o.x / 32) * 32 + 16 OR o.y <> floor(o.y / 32) * 32 + 16
        OR o.loiter_offset_x IS DISTINCT FROM -1 OR o.loiter_offset_y IS DISTINCT FROM 1
        OR o.current_state NOT IN ('open', 'closed');
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968h: % stall placement(s) off their tile centre, loiter or states', bad;
    END IF;
END $$;

COMMIT;
