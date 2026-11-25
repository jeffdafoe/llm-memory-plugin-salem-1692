-- LLM-968 down: the market stalls back on the Mana Seed village accessories
-- 80x96.png sheds and their old tiles.
--
-- ASSETS: render_scale 2, anchor 0.5/0.85, the pack rects (open row 0,
-- closed row 96; Wood x 0, Tiled x 80, Fancy x 160; 80x96), the debris slot
-- back at (40, 24), and each one's old footprint, door and stand:
--   Wood  footprint 2,2,5,0  door (-1, -3)  stand (-1, -1)
--   Tiled footprint 2,2,3,0  door (-2, -3)  stand (-1, -1)
--   Fancy footprint 2,2,5,0  door (-2, -4)  stand (-1, -2)
-- PLACEMENTS: each of the five stalls the up moved goes back to its exact
-- position and loiter offset from before the up, but only while it is still
-- exactly where the up left it (its asset, its tile's centre, loiter
-- (-2, 0)); one already back is accepted (a rerun); one moved or edited
-- since makes the down refuse rather than overwrite that edit. A stall
-- placed after the up keeps its position and loiter offset: the down has no
-- earlier value for it.
--
-- Each asset that exists is set, then checked; any difference fails the
-- migration. Convergent.

BEGIN;

LOCK TABLE asset, asset_state, asset_state_tag, asset_slot, village_object IN SHARE ROW EXCLUSIVE MODE;

DO $$
DECLARE
    bad integer;
BEGIN
    CREATE TEMP TABLE llm968h_down_stall (asset_id uuid PRIMARY KEY, x integer, fl integer, fr integer, ft integer,
                                          fb integer, dx integer, dy integer, sx integer, sy integer) ON COMMIT DROP;
    INSERT INTO llm968h_down_stall VALUES
        ('5073eb02-6f3f-4191-aced-242d8502d153', 0, 2, 2, 5, 0, -1, -3, -1, -1),     -- Market Stall (Wood)
        ('80c83812-f661-4f1d-8b70-99a5449814b2', 80, 2, 2, 3, 0, -2, -3, -1, -1),    -- Market Stall (Tiled)
        ('ab66597b-cf67-4a7a-a48f-d0fbc36117a4', 160, 2, 2, 5, 0, -2, -4, -1, -2);   -- Market Stall (Fancy)
    DELETE FROM llm968h_down_stall v WHERE NOT EXISTS (SELECT 1 FROM asset WHERE id = v.asset_id);
    IF NOT EXISTS (SELECT 1 FROM llm968h_down_stall) THEN
        RETURN;
    END IF;

    CREATE TEMP TABLE llm968h_down_state ON COMMIT DROP AS
    SELECT v.asset_id, s.state, '/tilesets/mana-seed/village-accessories/village accessories 80x96.png'::text AS sheet,
           v.x AS src_x, s.src_y, 80 AS src_w, 96 AS src_h, 1 AS frame_count, 0::double precision AS frame_rate, s.tag
      FROM llm968h_down_stall v
     CROSS JOIN (VALUES ('open', 0, 'occupied'), ('closed', 96, 'unoccupied')) s(state, src_y, tag);

    -- The five stalls: their asset, where the up left them (tile centre,
    -- loiter (-2, 0)) and where they were before it.
    CREATE TEMP TABLE llm968h_down_place (id uuid PRIMARY KEY, asset_id uuid, tx integer, ty integer,
                                          x double precision, y double precision, lx integer, ly integer) ON COMMIT DROP;
    INSERT INTO llm968h_down_place VALUES
        ('8e43e86f-29a2-465b-bd51-a54e2bc03c9a', '5073eb02-6f3f-4191-aced-242d8502d153', 23, 19,
         748.795837402344, 630.952819824219, -2, 1),        -- Blacksmith
        ('019dc1d5-3fe1-76fe-bc39-5d554b9bc61c', '5073eb02-6f3f-4191-aced-242d8502d153', 46, 10,
         1487.34094238281, 321.456726074219, -2, 1),        -- PW Apothecary
        ('019d79ef-d9df-7f74-b155-4b413d04c1bd', '80c83812-f661-4f1d-8b70-99a5449814b2', 35, 12,
         1125.890625, 404.86328125, -2, 1),                 -- General Store
        ('019e1390-0639-7bf6-8b66-08f95414079c', '80c83812-f661-4f1d-8b70-99a5449814b2', -30, -58,
         -940.864624023438, -1836.71960449219, -1, 1),      -- James Farm
        ('019e138d-724b-75d8-9374-9d931ebc93cd', 'ab66597b-cf67-4a7a-a48f-d0fbc36117a4', -18, -69,
         -550.988647460938, -2205.75732421875, -1, 1);      -- Ellis Farm
    DELETE FROM llm968h_down_place p WHERE NOT EXISTS (SELECT 1 FROM llm968h_down_stall v WHERE v.asset_id = p.asset_id);

    -- Refuse before writing anything: a known stall that is neither where the
    -- up left it nor already back where it was.
    SELECT count(*) INTO bad FROM village_object o JOIN llm968h_down_place p ON p.id = o.id
     WHERE (o.asset_id, o.x, o.y, o.loiter_offset_x, o.loiter_offset_y)
           IS DISTINCT FROM (p.asset_id, (p.tx * 32 + 16)::double precision, (p.ty * 32 + 16)::double precision, -2, 0)
       AND (o.asset_id, o.x, o.y, o.loiter_offset_x, o.loiter_offset_y)
           IS DISTINCT FROM (p.asset_id, p.x, p.y, p.lx, p.ly);
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968h down: % known stall placement(s) moved or edited since the up; not overwriting', bad;
    END IF;

    UPDATE asset a
       SET render_scale = 2, anchor_x = 0.5, anchor_y = 0.85,
           footprint_left = v.fl, footprint_right = v.fr, footprint_top = v.ft, footprint_bottom = v.fb,
           door_offset_x = v.dx, door_offset_y = v.dy, stand_offset_x = v.sx, stand_offset_y = v.sy
      FROM llm968h_down_stall v
     WHERE a.id = v.asset_id;
    UPDATE asset_state s
       SET sheet = v.sheet, src_x = v.src_x, src_y = v.src_y, src_w = v.src_w, src_h = v.src_h,
           frame_count = v.frame_count, frame_rate = v.frame_rate
      FROM llm968h_down_state v
     WHERE s.asset_id = v.asset_id AND s.state = v.state;
    UPDATE asset_slot sl SET offset_x = 40, offset_y = 24
      FROM llm968h_down_stall v
     WHERE sl.asset_id = v.asset_id AND sl.slot_name = 'debris';
    UPDATE village_object o
       SET x = p.x, y = p.y, loiter_offset_x = p.lx, loiter_offset_y = p.ly
      FROM llm968h_down_place p
     WHERE o.id = p.id;

    SELECT count(*) INTO bad FROM (
        (SELECT asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate FROM llm968h_down_state
         EXCEPT
         SELECT s.asset_id, s.state, s.sheet, s.src_x, s.src_y, s.src_w, s.src_h, s.frame_count, s.frame_rate
           FROM asset_state s JOIN llm968h_down_stall v ON v.asset_id = s.asset_id)
        UNION ALL
        (SELECT s.asset_id, s.state, s.sheet, s.src_x, s.src_y, s.src_w, s.src_h, s.frame_count, s.frame_rate
           FROM asset_state s JOIN llm968h_down_stall v ON v.asset_id = s.asset_id
         EXCEPT
         SELECT asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate FROM llm968h_down_state)
    ) diff;
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968h down: % stall state rows differ from the pack values', bad;
    END IF;
    SELECT count(*) INTO bad FROM (
        (SELECT asset_id, state, tag FROM llm968h_down_state
         EXCEPT
         SELECT s.asset_id, s.state::text, t.tag::text
           FROM asset_state s JOIN llm968h_down_stall v ON v.asset_id = s.asset_id JOIN asset_state_tag t ON t.state_id = s.id)
        UNION ALL
        (SELECT s.asset_id, s.state::text, t.tag::text
           FROM asset_state s JOIN llm968h_down_stall v ON v.asset_id = s.asset_id JOIN asset_state_tag t ON t.state_id = s.id
         EXCEPT
         SELECT asset_id, state, tag FROM llm968h_down_state)
    ) diff;
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968h down: % stall state tags differ from open occupied / closed unoccupied', bad;
    END IF;
    SELECT count(*) INTO bad FROM asset a JOIN llm968h_down_stall v ON v.asset_id = a.id
     WHERE (a.render_scale, a.anchor_x, a.anchor_y, a.footprint_left, a.footprint_right, a.footprint_top,
            a.footprint_bottom, a.door_offset_x, a.door_offset_y, a.stand_offset_x, a.stand_offset_y)
           IS DISTINCT FROM (2::double precision, 0.5::double precision, 0.85::double precision, v.fl, v.fr, v.ft, v.fb,
                             v.dx, v.dy, v.sx, v.sy)
        OR (SELECT count(*) FROM asset_slot sl WHERE sl.asset_id = a.id) <> 1
        OR NOT EXISTS (SELECT 1 FROM asset_slot sl WHERE sl.asset_id = a.id AND sl.slot_name = 'debris'
                                                     AND sl.offset_x = 40 AND sl.offset_y = 24);
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968h down: % stall asset(s) differ from the pack columns or slot', bad;
    END IF;
    SELECT count(*) INTO bad FROM village_object o JOIN llm968h_down_place p ON p.id = o.id
     WHERE (o.asset_id, o.x, o.y, o.loiter_offset_x, o.loiter_offset_y) IS DISTINCT FROM (p.asset_id, p.x, p.y, p.lx, p.ly);
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968h down: % known stall placement(s) not back where they were', bad;
    END IF;
END $$;

COMMIT;
