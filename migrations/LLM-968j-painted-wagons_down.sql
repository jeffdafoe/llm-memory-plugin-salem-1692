-- LLM-968 down: the wagons back on the Mana Seed village accessories sheets
-- at render_scale 2 (anchor 0.5/0.85, the pack rects) and their old tiles:
--   Wagon          category structure, footprint 2,2,5,0, no door
--   Covered Wagon  category prop,      footprint 2,2,4,1, door (-2, -2)
--   Cart           category prop,      no footprint
-- PLACEMENTS: the covered wagon the up moved goes back to its exact position
-- from before the up, but only while it is still where the up left it (its
-- tile's centre); one already back is accepted (a rerun); one moved since
-- makes the down refuse rather than overwrite that edit. A wagon placed after
-- the up keeps its position.
--
-- Refuses first if the old, taller footprints would take back a tile that is
-- not clear (see below).
--
-- Each asset that exists is set, then checked; any difference fails the
-- migration. Convergent.

BEGIN;

LOCK TABLE asset, asset_state, asset_state_tag, asset_state_light, asset_slot, village_object, village_terrain
    IN SHARE ROW EXCLUSIVE MODE;

DO $$
DECLARE
    bad integer;
    a80 CONSTANT text := '/tilesets/mana-seed/village-accessories/village accessories 80x96.png';
    a48 CONSTANT text := '/tilesets/mana-seed/village-accessories/village accessories 48x80.png';
BEGIN
    CREATE TEMP TABLE llm968j_down_asset (asset_id uuid PRIMARY KEY, category text, sheet text, src_x integer,
                                          src_y integer, src_w integer, src_h integer, fl integer, fr integer,
                                          ft integer, fb integer, dx integer, dy integer) ON COMMIT DROP;
    INSERT INTO llm968j_down_asset VALUES
        ('7e10da69-cdc2-4c48-9b9b-b4ad10dc839c', 'structure', a80, 80, 192, 80, 96, 2, 2, 5, 0, NULL, NULL),  -- Wagon
        ('3500f9ce-59be-45c1-9917-50bf0e232f4e', 'prop', a80, 160, 192, 80, 96, 2, 2, 4, 1, -2, -2),          -- Covered Wagon
        ('11483134-ab6c-4fe3-9b41-83bb29663d80', 'prop', a48, 240, 0, 48, 80, 0, 0, 0, 0, NULL, NULL);        -- Cart
    DELETE FROM llm968j_down_asset v WHERE NOT EXISTS (SELECT 1 FROM asset WHERE id = v.asset_id);
    IF NOT EXISTS (SELECT 1 FROM llm968j_down_asset) THEN
        RETURN;
    END IF;

    -- The covered wagon: where the up left it (its tile's centre) and where
    -- it was before.
    CREATE TEMP TABLE llm968j_down_place (id uuid PRIMARY KEY, asset_id uuid, tx integer, ty integer,
                                          x double precision, y double precision) ON COMMIT DROP;
    INSERT INTO llm968j_down_place VALUES
        ('019d79ef-d9df-7c88-943f-b846174bca65', '3500f9ce-59be-45c1-9917-50bf0e232f4e', 28, 5,
         898.426330566406, 187.881195068359);
    DELETE FROM llm968j_down_place p WHERE NOT EXISTS (SELECT 1 FROM llm968j_down_asset v WHERE v.asset_id = p.asset_id);

    SELECT count(*) INTO bad FROM village_object o JOIN llm968j_down_place p ON p.id = o.id
     WHERE (o.asset_id, o.x, o.y)
           IS DISTINCT FROM (p.asset_id, (p.tx * 32 + 16)::double precision, (p.ty * 32 + 16)::double precision)
       AND (o.asset_id, o.x, o.y) IS DISTINCT FROM (p.asset_id, p.x, p.y);
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968j down: the covered wagon was moved or edited since the up; not overwriting';
    END IF;

    -- Refuse before writing anything if a placed wagon's old footprint would
    -- take back a tile that is not clear. The old footprints reach rows above
    -- the new one (2,2,1,1) that a wagon placed after the up was never checked
    -- on, and that any wagon may have gained a neighbour on since: rows -5..-2
    -- (Wagon) and -4..-2 (Covered Wagon), columns -2..2. (The Wagon's row +1
    -- goes back to walkable, nothing to check.) A tile is not clear when it
    -- is under another object's footprint as it will be AFTER this down (the
    -- other wagons widen too; a non-obstacle counts as well, as in the camp's
    -- down: a crate or bush the wagon grew over would be buried in blocked
    -- ground, out of reach), on water, or off the map. Tiles are the
    -- engine's: floor(world / 32) plus the pad (60, 112) on the 200 x 180
    -- terrain grid; terrain 5 and 6 are water. The known covered wagon goes
    -- back to a spot on the same tile, so its current tile stands for it.
    SELECT count(*) INTO bad
      FROM village_object t
      JOIN llm968j_down_asset ta ON ta.asset_id = t.asset_id AND ta.ft > 0
     CROSS JOIN LATERAL (
           SELECT floor(t.x / 32)::int + dx AS tx, floor(t.y / 32)::int + dy AS ty
             FROM generate_series(-2, 2) dx CROSS JOIN generate_series(-ta.ft, -2) dy) strip
     WHERE t.attached_to IS NULL
       AND (NOT (strip.tx + 60 BETWEEN 0 AND 199 AND strip.ty + 112 BETWEEN 0 AND 179)
            OR EXISTS (SELECT 1 FROM village_object o JOIN asset oa ON oa.id = o.asset_id
                        LEFT JOIN llm968j_down_asset ov ON ov.asset_id = o.asset_id
                        -- The footprint each object will have after this down.
                        CROSS JOIN LATERAL (SELECT CASE WHEN ov.asset_id IS NOT NULL
                                                        THEN ARRAY[ov.fl, ov.fr, ov.ft, ov.fb]
                                                        ELSE ARRAY[oa.footprint_left, oa.footprint_right,
                                                                   oa.footprint_top, oa.footprint_bottom] END AS fp) f
                        WHERE o.id <> t.id AND o.attached_to IS NULL
                          AND strip.tx BETWEEN floor(o.x / 32)::int - f.fp[1] AND floor(o.x / 32)::int + f.fp[2]
                          AND strip.ty BETWEEN floor(o.y / 32)::int - f.fp[3] AND floor(o.y / 32)::int + f.fp[4])
            OR EXISTS (SELECT 1 FROM village_terrain vt
                        WHERE vt.id = 1 AND strip.tx + 60 BETWEEN 0 AND 199 AND strip.ty + 112 BETWEEN 0 AND 179
                          AND octet_length(vt.data) >= 200 * 180
                          AND get_byte(vt.data, (strip.ty + 112) * 200 + (strip.tx + 60)) IN (5, 6)));
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968j down: % tile(s) the old wagon footprints would take back are not clear; move those wagons first', bad;
    END IF;

    UPDATE asset a
       SET category = v.category, render_scale = 2, anchor_x = 0.5, anchor_y = 0.85,
           footprint_left = v.fl, footprint_right = v.fr, footprint_top = v.ft, footprint_bottom = v.fb,
           door_offset_x = v.dx, door_offset_y = v.dy
      FROM llm968j_down_asset v
     WHERE a.id = v.asset_id;
    UPDATE asset_state s
       SET sheet = v.sheet, src_x = v.src_x, src_y = v.src_y, src_w = v.src_w, src_h = v.src_h,
           frame_count = 1, frame_rate = 0
      FROM llm968j_down_asset v
     WHERE s.asset_id = v.asset_id AND s.state = 'default';
    UPDATE village_object o
       SET x = p.x, y = p.y
      FROM llm968j_down_place p
     WHERE o.id = p.id AND o.asset_id = p.asset_id;

    SELECT count(*) INTO bad FROM (
        (SELECT asset_id, 'default'::text AS state, sheet, src_x, src_y, src_w, src_h, 1 AS frame_count,
                0::double precision AS frame_rate FROM llm968j_down_asset
         EXCEPT
         SELECT s.asset_id, s.state::text, s.sheet::text, s.src_x, s.src_y, s.src_w, s.src_h, s.frame_count, s.frame_rate
           FROM asset_state s JOIN llm968j_down_asset v ON v.asset_id = s.asset_id)
        UNION ALL
        (SELECT s.asset_id, s.state::text, s.sheet::text, s.src_x, s.src_y, s.src_w, s.src_h, s.frame_count, s.frame_rate
           FROM asset_state s JOIN llm968j_down_asset v ON v.asset_id = s.asset_id
         EXCEPT
         SELECT asset_id, 'default'::text, sheet, src_x, src_y, src_w, src_h, 1, 0::double precision FROM llm968j_down_asset)
    ) diff;
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968j down: % wagon state rows differ from the pack values', bad;
    END IF;
    SELECT count(*) INTO bad FROM asset a JOIN llm968j_down_asset v ON v.asset_id = a.id
     WHERE (a.category::text, a.render_scale, a.anchor_x, a.anchor_y, a.footprint_left, a.footprint_right,
            a.footprint_top, a.footprint_bottom, a.door_offset_x, a.door_offset_y)
           IS DISTINCT FROM (v.category, 2::double precision, 0.5::double precision, 0.85::double precision,
                             v.fl, v.fr, v.ft, v.fb, v.dx, v.dy);
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968j down: % wagon asset(s) differ from the pack columns', bad;
    END IF;
    SELECT count(*) INTO bad FROM village_object o JOIN llm968j_down_place p ON p.id = o.id
     WHERE (o.asset_id, o.x, o.y) IS DISTINCT FROM (p.asset_id, p.x, p.y);
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968j down: the covered wagon is not back where it was';
    END IF;
END $$;

COMMIT;
