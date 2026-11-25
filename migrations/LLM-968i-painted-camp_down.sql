-- LLM-968 down: the camp back on the Mana Seed travelers-camp sheets at
-- render_scale 2 (the pack rects, the Campfire's 5-frame states), the three
-- tents back on their old footprint 1,2,3,0 and doors (Tent (0, 0), Tent
-- (Closed) (-1, -2), Tent (Open) (1, -1)), and the Camp Fire's two lights
-- removed; the Campfire's light stays.
--
-- This is a reset to those values, not an undo of whatever the up found: the
-- up keeps no record of the values it overwrote.
--
-- Each asset that exists is set, then checked both ways; any difference fails
-- the migration. Convergent.

BEGIN;

LOCK TABLE asset, asset_state, asset_state_tag, asset_state_light, village_object IN SHARE ROW EXCLUSIVE MODE;

DO $$
DECLARE
    bad integer;
    c32 CONSTANT text := '/tilesets/mana-seed/travelers-camp/travelers camp 32x32.png';
    t16 CONSTANT text := '/tilesets/mana-seed/travelers-camp/travelers camp tent 16x16.png';
    t64 CONSTANT text := '/tilesets/mana-seed/travelers-camp/travelers camp tent 64x64.png';
BEGIN
    CREATE TEMP TABLE llm968i_down_state (asset_id uuid, state text, sheet text, src_x integer, src_y integer,
                                          src_w integer, src_h integer, frame_count integer,
                                          frame_rate double precision) ON COMMIT DROP;
    INSERT INTO llm968i_down_state VALUES
        ('019e5f00-c401-7a10-9e00-000000798001', 'closed', t64, 64, 0, 64, 64, 1, 0),
        ('019e5f00-c401-7a10-9e00-000000798001', 'open', t64, 0, 0, 64, 64, 1, 0),
        ('019e5f00-c401-7a10-9e00-000000798001', 'packed', t16, 32, 0, 16, 16, 1, 0),
        ('019e5f00-c401-7a10-9e00-000000798002', 'unlit', c32, 0, 0, 32, 32, 1, 0),
        ('019e5f00-c401-7a10-9e00-000000798002', 'burning', c32, 32, 0, 32, 32, 4, 8),
        ('019e5f00-c401-7a10-9e00-000000798002', 'dying', c32, 32, 32, 32, 32, 4, 6),
        ('019e5f00-c401-7a10-9e00-000000798002', 'ashes', c32, 0, 32, 32, 32, 1, 0),
        ('019e5f00-c401-7a10-9e00-000000798003', 'default', t16, 0, 0, 16, 16, 1, 0),
        ('019e5f00-c401-7a10-9e00-000000798004', 'default', t16, 16, 0, 16, 16, 1, 0),
        ('18118703-3aa8-4f28-8ffd-3d3c9f0804ed', 'default', t64, 64, 0, 64, 64, 1, 0),
        ('a6f9d413-385f-43e6-9d19-b91f49bc343c', 'default', t64, 0, 0, 64, 64, 1, 0),
        ('3b42aadd-dc7e-411c-90cc-f3d8395a7c32', 'default', c32, 0, 0, 32, 32, 5, 8),
        ('3b42aadd-dc7e-411c-90cc-f3d8395a7c32', 'unlit', c32, 0, 32, 32, 32, 5, 8),
        ('95021559-d659-476f-888e-cc0692f89cba', 'default', t16, 16, 0, 16, 16, 1, 0),
        ('daa827fd-9cd8-4958-98e3-aed4953b9ec8', 'default', t16, 32, 0, 16, 16, 1, 0),
        ('3d079da4-2e38-42dc-abc4-583254bf197a', 'closed', c32, 32, 64, 32, 32, 1, 0),
        ('3d079da4-2e38-42dc-abc4-583254bf197a', 'open', c32, 64, 64, 32, 32, 1, 0),
        ('74ec990c-9710-4444-9817-7bfccdb200f7', 'default', c32, 96, 64, 32, 32, 1, 0),
        ('74ec990c-9710-4444-9817-7bfccdb200f7', 'empty', c32, 128, 64, 32, 32, 1, 0),
        ('562b3d35-6164-41dd-8ed5-0c66dd837b19', 'default', c32, 0, 64, 32, 32, 1, 0);
    DELETE FROM llm968i_down_state v WHERE NOT EXISTS (SELECT 1 FROM asset WHERE id = v.asset_id);
    IF NOT EXISTS (SELECT 1 FROM llm968i_down_state) THEN
        RETURN;
    END IF;
    CREATE TEMP TABLE llm968i_down_asset ON COMMIT DROP AS SELECT DISTINCT asset_id FROM llm968i_down_state;

    CREATE TEMP TABLE llm968i_down_tag (asset_id uuid, state text, tag text) ON COMMIT DROP;
    INSERT INTO llm968i_down_tag
    SELECT a, s, t FROM (VALUES
        ('019e5f00-c401-7a10-9e00-000000798001'::uuid, 'closed', 'unoccupied'),
        ('019e5f00-c401-7a10-9e00-000000798001'::uuid, 'open', 'occupied'),
        ('3b42aadd-dc7e-411c-90cc-f3d8395a7c32'::uuid, 'default', 'night-active'),
        ('3b42aadd-dc7e-411c-90cc-f3d8395a7c32'::uuid, 'unlit', 'day-active')) v(a, s, t)
     WHERE EXISTS (SELECT 1 FROM llm968i_down_asset x WHERE x.asset_id = v.a);

    CREATE TEMP TABLE llm968i_down_light (asset_id uuid, state text, color text, radius integer,
                                          energy double precision, offset_x integer, offset_y integer,
                                          flicker_amplitude double precision, flicker_period_ms integer) ON COMMIT DROP;
    INSERT INTO llm968i_down_light
    SELECT * FROM (VALUES
        ('3b42aadd-dc7e-411c-90cc-f3d8395a7c32'::uuid, 'default', '#FF8833', 128, 1::double precision, 0, 0,
         0.15::double precision, 180)) v
     WHERE EXISTS (SELECT 1 FROM llm968i_down_asset x WHERE x.asset_id = v.column1);

    -- Refuse before writing anything if widening a placed tent back to the
    -- old footprint (1,2,3,0) would cover a tile that is not clear: the strip
    -- the old footprint adds to the new one (1,1,2,0) — the column to the
    -- right and the row on top — under another object's footprint, or on
    -- water. A camp pitched after the up was ground-checked only on the new
    -- footprint, and any camp may have gained a neighbour there since.
    -- Tiles are the engine's: floor(world / 32) plus the pad (60, 112) on a
    -- 200-wide terrain grid; terrain 5 and 6 are water.
    SELECT count(*) INTO bad
      FROM village_object t
      JOIN asset ta ON ta.id = t.asset_id
     CROSS JOIN LATERAL (
           SELECT floor(t.x / 32)::int + dx AS tx, floor(t.y / 32)::int + dy AS ty
             FROM generate_series(-1, 2) dx CROSS JOIN generate_series(-3, 0) dy
            WHERE NOT (dx BETWEEN -1 AND 1 AND dy BETWEEN -2 AND 0)) strip
     WHERE ta.id IN ('019e5f00-c401-7a10-9e00-000000798001', '18118703-3aa8-4f28-8ffd-3d3c9f0804ed',
                     'a6f9d413-385f-43e6-9d19-b91f49bc343c')
       AND t.attached_to IS NULL
       AND (EXISTS (SELECT 1 FROM village_object o JOIN asset oa ON oa.id = o.asset_id
                     WHERE o.id <> t.id AND o.attached_to IS NULL
                       AND strip.tx BETWEEN floor(o.x / 32)::int - oa.footprint_left AND floor(o.x / 32)::int + oa.footprint_right
                       AND strip.ty BETWEEN floor(o.y / 32)::int - oa.footprint_top AND floor(o.y / 32)::int + oa.footprint_bottom)
            OR EXISTS (SELECT 1 FROM village_terrain vt
                        WHERE vt.id = 1 AND get_byte(vt.data, (strip.ty + 112) * 200 + (strip.tx + 60)) IN (5, 6)));
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968i down: % tile(s) the old tent footprint would take back are not clear; strike or move those tents first', bad;
    END IF;

    UPDATE asset a SET render_scale = 2 FROM llm968i_down_asset v WHERE a.id = v.asset_id;
    UPDATE asset a
       SET footprint_left = 1, footprint_right = 2, footprint_top = 3, footprint_bottom = 0,
           door_offset_x = d.x, door_offset_y = d.y
      FROM (VALUES ('019e5f00-c401-7a10-9e00-000000798001'::uuid, 0, 0),
                   ('18118703-3aa8-4f28-8ffd-3d3c9f0804ed'::uuid, -1, -2),
                   ('a6f9d413-385f-43e6-9d19-b91f49bc343c'::uuid, 1, -1)) d(id, x, y)
     WHERE a.id = d.id;
    UPDATE asset_state s
       SET sheet = v.sheet, src_x = v.src_x, src_y = v.src_y, src_w = v.src_w, src_h = v.src_h,
           frame_count = v.frame_count, frame_rate = v.frame_rate
      FROM llm968i_down_state v
     WHERE s.asset_id = v.asset_id AND s.state = v.state;
    -- Only the two lights the up wrote; any other light on the Camp Fire
    -- fails the check below.
    DELETE FROM asset_state_light l USING asset_state s
     WHERE l.state_id = s.id AND s.asset_id = '019e5f00-c401-7a10-9e00-000000798002'
       AND s.state IN ('burning', 'dying');

    SELECT count(*) INTO bad FROM (
        (SELECT asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate FROM llm968i_down_state
         EXCEPT
         SELECT s.asset_id, s.state, s.sheet, s.src_x, s.src_y, s.src_w, s.src_h, s.frame_count, s.frame_rate
           FROM asset_state s JOIN llm968i_down_asset v ON v.asset_id = s.asset_id)
        UNION ALL
        (SELECT s.asset_id, s.state, s.sheet, s.src_x, s.src_y, s.src_w, s.src_h, s.frame_count, s.frame_rate
           FROM asset_state s JOIN llm968i_down_asset v ON v.asset_id = s.asset_id
         EXCEPT
         SELECT asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate FROM llm968i_down_state)
    ) diff;
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968i down: % camp state rows differ from the pack values', bad;
    END IF;
    SELECT count(*) INTO bad FROM (
        (SELECT asset_id, state, tag FROM llm968i_down_tag
         EXCEPT
         SELECT s.asset_id, s.state::text, t.tag::text
           FROM asset_state s JOIN llm968i_down_asset v ON v.asset_id = s.asset_id JOIN asset_state_tag t ON t.state_id = s.id)
        UNION ALL
        (SELECT s.asset_id, s.state::text, t.tag::text
           FROM asset_state s JOIN llm968i_down_asset v ON v.asset_id = s.asset_id JOIN asset_state_tag t ON t.state_id = s.id
         EXCEPT
         SELECT asset_id, state, tag FROM llm968i_down_tag)
    ) diff;
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968i down: % camp state tags differ from the expected ones', bad;
    END IF;
    SELECT count(*) INTO bad FROM (
        (SELECT * FROM llm968i_down_light
         EXCEPT
         SELECT s.asset_id, s.state::text, l.color::text, l.radius, l.energy, l.offset_x, l.offset_y,
                l.flicker_amplitude, l.flicker_period_ms
           FROM asset_state s JOIN llm968i_down_asset v ON v.asset_id = s.asset_id JOIN asset_state_light l ON l.state_id = s.id)
        UNION ALL
        (SELECT s.asset_id, s.state::text, l.color::text, l.radius, l.energy, l.offset_x, l.offset_y,
                l.flicker_amplitude, l.flicker_period_ms
           FROM asset_state s JOIN llm968i_down_asset v ON v.asset_id = s.asset_id JOIN asset_state_light l ON l.state_id = s.id
         EXCEPT
         SELECT * FROM llm968i_down_light)
    ) diff;
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968i down: % camp lights differ from the expected values', bad;
    END IF;
    SELECT count(*) INTO bad FROM asset a JOIN llm968i_down_asset v ON v.asset_id = a.id
     WHERE a.render_scale IS DISTINCT FROM 2::double precision
        OR (a.id = '019e5f00-c401-7a10-9e00-000000798001'
            AND (a.footprint_left, a.footprint_right, a.footprint_top, a.footprint_bottom, a.door_offset_x, a.door_offset_y)
                IS DISTINCT FROM (1, 2, 3, 0, 0, 0))
        OR (a.id = '18118703-3aa8-4f28-8ffd-3d3c9f0804ed'
            AND (a.footprint_left, a.footprint_right, a.footprint_top, a.footprint_bottom, a.door_offset_x, a.door_offset_y)
                IS DISTINCT FROM (1, 2, 3, 0, -1, -2))
        OR (a.id = 'a6f9d413-385f-43e6-9d19-b91f49bc343c'
            AND (a.footprint_left, a.footprint_right, a.footprint_top, a.footprint_bottom, a.door_offset_x, a.door_offset_y)
                IS DISTINCT FROM (1, 2, 3, 0, 1, -1));
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968i down: % camp asset(s) differ from the pack columns', bad;
    END IF;
END $$;

COMMIT;
