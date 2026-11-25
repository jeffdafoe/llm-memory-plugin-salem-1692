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
-- (visible_when_inside) on the stand tile, and villagers and objects share
-- one y-sorted layer (z_index 10 both), so the keeper shows only through the
-- stall's empty pixels when the keeper's y is less than the stall's. The pack
-- sheds showed the keeper through a window that had to line up with the
-- stand tile, from placements anywhere in their anchor tile, so the keeper
-- stood at a different spot in each stall's art. The booth leaves the whole
-- space between counter and roof empty, and the placements move to their
-- anchor tile's centre, so every keeper stands at the same spot in the art.
--
-- ASSETS (the three, live-only: no migration creates them), the same tiles
-- on all three:
--   render_scale 1; anchor 0.5 / (92/112): the anchor tile's centre, a tile
--   below the keeper's feet;
--   stand (0, -1): the row behind the anchor, so the keeper's y is less than
--   the stall's wherever the stall sits in its tile, and the keeper is always
--   drawn first (on the anchor's own row the two would tie, and a tie goes
--   to tree order, which draws villagers last: in front of the counter);
--   footprint left 1, right 1, top 2, bottom 0 (3 x 3; was 5 x 4..6, wider
--   than the art): every new footprint lies inside its old one, so no tile
--   becomes blocked;
--   door (0, -2): the back centre, on the footprint's north edge, so the walk
--   grid opens it north (pathfind.go doorExitSide) and the keeper comes in
--   from behind the counter; it is the footprint's one walkable tile;
--   states open (x 0) and closed (x 128), 128x224, one frame each;
--   the debris slot (world px from the anchor) moves from (40, 24) to
--   (40, -8), the same spot on the art (the anchor moved a tile down on it):
--   the storm's debris lies in front of the booth's right corner.
-- Unchanged and checked: the tags (open occupied, closed unoccupied: the
-- stall shows open while someone is in it) and visible_when_inside true.
--
-- PLACEMENTS: the five known stalls, by id, each staged with the anchor tile
-- it is on now. Each moves to that tile's centre (at most half a tile; the
-- anchor tile, so every tile-based rule, is unchanged) and takes loiter
-- offset (-2, 0): customers front-left of the long counter, clear of the art
-- (straight in front a customer hides the keeper). Any other placement on the
-- three assets, or a known one on a different tile, makes the migration
-- refuse: the tiles below were checked for these five on these tiles only.
-- Checked against the live map with the engine's walk-grid rules before
-- writing this: at all five, the new door, the three tiles north of it, the
-- loiter tile and its ring are dry ground under no other object's footprint.
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
-- whatever is there; it keeps nothing to restore: the down carries the five
-- stalls' values from before).

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

    -- The five stalls: their asset, the anchor tile they are on (world px /
    -- 32, floored), and so their centre.
    CREATE TEMP TABLE llm968h_place (id uuid PRIMARY KEY, asset_id uuid, tx integer, ty integer) ON COMMIT DROP;
    INSERT INTO llm968h_place VALUES
        ('8e43e86f-29a2-465b-bd51-a54e2bc03c9a', '5073eb02-6f3f-4191-aced-242d8502d153', 23, 19),    -- Blacksmith
        ('019dc1d5-3fe1-76fe-bc39-5d554b9bc61c', '5073eb02-6f3f-4191-aced-242d8502d153', 46, 10),    -- PW Apothecary
        ('019d79ef-d9df-7f74-b155-4b413d04c1bd', '80c83812-f661-4f1d-8b70-99a5449814b2', 35, 12),    -- General Store
        ('019e1390-0639-7bf6-8b66-08f95414079c', '80c83812-f661-4f1d-8b70-99a5449814b2', -30, -58),  -- James Farm
        ('019e138d-724b-75d8-9374-9d931ebc93cd', 'ab66597b-cf67-4a7a-a48f-d0fbc36117a4', -18, -69);  -- Ellis Farm
    DELETE FROM llm968h_place p WHERE NOT EXISTS (SELECT 1 FROM llm968h_stall v WHERE v.asset_id = p.asset_id);

    -- Refuse before writing anything: a placement on the three assets that
    -- is not one of the five, or one of the five on another asset or tile.
    SELECT count(*) INTO bad FROM village_object o JOIN llm968h_stall v ON v.asset_id = o.asset_id
     WHERE NOT EXISTS (SELECT 1 FROM llm968h_place p
                        WHERE p.id = o.id AND p.asset_id = o.asset_id
                          AND p.tx = floor(o.x / 32) AND p.ty = floor(o.y / 32));
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968h: % stall placement(s) not among the five checked stalls on their checked tiles', bad;
    END IF;

    CREATE TEMP TABLE llm968h_state ON COMMIT DROP AS
    SELECT v.asset_id, s.state, v.sheet, s.src_x, 0 AS src_y, 128 AS src_w, 224 AS src_h, 1 AS frame_count,
           0::double precision AS frame_rate, s.tag
      FROM llm968h_stall v
     CROSS JOIN (VALUES ('open', 0, 'occupied'), ('closed', 128, 'unoccupied')) s(state, src_x, tag);

    UPDATE asset a
       SET render_scale = 1, anchor_x = 0.5, anchor_y = 92.0::double precision / 112,
           footprint_left = 1, footprint_right = 1, footprint_top = 2, footprint_bottom = 0,
           door_offset_x = 0, door_offset_y = -2, stand_offset_x = 0, stand_offset_y = -1
      FROM llm968h_stall v
     WHERE a.id = v.asset_id;
    -- Existing states only: the check below fails on a missing one.
    UPDATE asset_state s
       SET sheet = v.sheet, src_x = v.src_x, src_y = v.src_y, src_w = v.src_w, src_h = v.src_h,
           frame_count = v.frame_count, frame_rate = v.frame_rate
      FROM llm968h_state v
     WHERE s.asset_id = v.asset_id AND s.state = v.state;
    UPDATE asset_slot sl SET offset_x = 40, offset_y = -8
      FROM llm968h_stall v
     WHERE sl.asset_id = v.asset_id AND sl.slot_name = 'debris';
    UPDATE village_object o
       SET x = p.tx * 32 + 16, y = p.ty * 32 + 16, loiter_offset_x = -2, loiter_offset_y = 0
      FROM llm968h_place p
     WHERE o.id = p.id;

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
           IS DISTINCT FROM (1::double precision, 0.5::double precision, 92.0::double precision / 112, 1, 1, 2, 0,
                             0, -2, 0, -1, true);
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968h: % stall asset(s) differ from the authored columns', bad;
    END IF;
    SELECT count(*) INTO bad FROM llm968h_stall v
     WHERE (SELECT count(*) FROM asset_slot sl WHERE sl.asset_id = v.asset_id) <> 1
        OR NOT EXISTS (SELECT 1 FROM asset_slot sl WHERE sl.asset_id = v.asset_id AND sl.slot_name = 'debris'
                                                     AND sl.offset_x = 40 AND sl.offset_y = -8);
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968h: % stall asset(s) without exactly their debris slot at (40, -8)', bad;
    END IF;
    -- Every placement on the three is one of the five, at exactly its staged
    -- centre and loiter, on a state the assets have.
    SELECT count(*) INTO bad FROM village_object o JOIN llm968h_stall v ON v.asset_id = o.asset_id
      LEFT JOIN llm968h_place p ON p.id = o.id
     WHERE p.id IS NULL
        OR (o.asset_id, o.x, o.y, o.loiter_offset_x, o.loiter_offset_y)
           IS DISTINCT FROM (p.asset_id, (p.tx * 32 + 16)::double precision, (p.ty * 32 + 16)::double precision, -2, 0)
        OR o.current_state IS NULL OR o.current_state NOT IN ('open', 'closed');
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968h: % stall placement(s) not at their staged centre, loiter and state', bad;
    END IF;
END $$;

COMMIT;
