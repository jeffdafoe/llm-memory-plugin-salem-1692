-- LLM-968: painted wagons: the Wagon, the Covered Wagon and the Cart.
--
-- They drew Mana Seed village accessories 80x96.png and 48x80.png, whose
-- licence forbids use alongside AI. tools/tree-art/wagons.py now paints them:
-- client/assets/trees/wagons.png (wagon x 0, covered wagon x 192; 192x192
-- frames) and cart.png (128x96), shown at render_scale 1. Jeff approved the
-- look, the footprint and dropping the covered wagon's door on 2026-11-27.
--
-- ASSETS (live-only: no migration creates them):
--   Wagon and Covered Wagon: anchor 44/96, 58/96 (the middle of the box's
--   ground; the pack anchor sat at the front of the drawing), footprint
--   left 2, right 2, top 1, bottom 1 (5 x 3, the box and its wheels; the
--   tongue lies outside it, ground a walker steps over). They were 2,2,5,0
--   (Wagon) and 2,2,4,1 (Covered Wagon).
--   The Covered Wagon's door (-2, -2) is cleared. It is a prop (LLM-456) and
--   nobody goes into it: the door only opened a walkable hole in its blocked
--   tiles, and it would lie outside the new footprint. The one placement's
--   entry_policy (open, set at placement because the asset had a door) stays:
--   the engine reads it only for a structure, and this is a bare object.
--   The Wagon's category goes from structure to prop, as LLM-456 did for the
--   Covered Wagon: a structure-category asset dropped in the editor is
--   promoted to a Structure, and a wagon is not a building.
--   Cart: anchor 26/64, 27/48 (the middle of the box's ground), no footprint
--   and not an obstacle, as before.
--   Each has one state, default, one frame.
--
-- PLACEMENTS: the one Covered Wagon, by id, on its anchor tile (28, 5), a
-- bare object (not attached, no structure), moves to that tile's centre (at
-- most half a tile, the anchor tile and so every tile rule unchanged). No
-- tile becomes blocked: its old walk-grid tiles were the 5 x 6 footprint less
-- the door tile (-2, -2) (on the footprint's west edge, so no corridor); the
-- new ones are the 5 x 3 box, all inside the old blocked tiles, and the old
-- door tile is now outside the footprint (engine/sim/
-- painted_wagon_walkgrid_test.go builds both grids). Any other placement of
-- the Wagon or the Covered Wagon, or the known one attached, promoted to a
-- structure, moved off its tile or onto another asset, makes the migration
-- refuse: the Wagon's new footprint reaches a row below its anchor that its
-- old one did not, and only this placement was checked. So after a rollback
-- that keeps a wagon placed in between, the next up refuses until it is
-- removed. Cart placements (none live) are not moved: no footprint.
--
-- Each asset that exists is set, then checked exactly (columns, states both
-- ways, no tags, no lights, no slots) along with the placement; any
-- difference fails the migration. ENGINE-OWNED data, read at boot; deploy.sh
-- runs stop -> migrate -> start. Convergent: a rerun ends in the same place
-- and repairs a hand edit.

BEGIN;

LOCK TABLE asset, asset_state, asset_state_tag, asset_state_light, asset_slot, village_object, structure
    IN SHARE ROW EXCLUSIVE MODE;

DO $$
DECLARE
    bad integer;
BEGIN
    CREATE TEMP TABLE llm968j_asset (asset_id uuid PRIMARY KEY, sheet text, src_x integer, src_w integer, src_h integer,
                                     anchor_x double precision, anchor_y double precision,
                                     fl integer, fr integer, ft integer, fb integer, is_obstacle boolean) ON COMMIT DROP;
    INSERT INTO llm968j_asset VALUES
        ('7e10da69-cdc2-4c48-9b9b-b4ad10dc839c', 'res://assets/trees/wagons.png', 0, 192, 192,
         44.0::double precision / 96, 58.0::double precision / 96, 2, 2, 1, 1, true),     -- Wagon
        ('3500f9ce-59be-45c1-9917-50bf0e232f4e', 'res://assets/trees/wagons.png', 192, 192, 192,
         44.0::double precision / 96, 58.0::double precision / 96, 2, 2, 1, 1, true),     -- Covered Wagon
        ('11483134-ab6c-4fe3-9b41-83bb29663d80', 'res://assets/trees/cart.png', 0, 128, 96,
         26.0::double precision / 64, 27.0::double precision / 48, 0, 0, 0, 0, false);    -- Cart
    DELETE FROM llm968j_asset v WHERE NOT EXISTS (SELECT 1 FROM asset WHERE id = v.asset_id);
    IF NOT EXISTS (SELECT 1 FROM llm968j_asset) THEN
        RETURN;
    END IF;

    -- The one placed Covered Wagon and its anchor tile (world px / 32,
    -- floored).
    CREATE TEMP TABLE llm968j_place (id uuid PRIMARY KEY, asset_id uuid, tx integer, ty integer) ON COMMIT DROP;
    INSERT INTO llm968j_place VALUES
        ('019d79ef-d9df-7c88-943f-b846174bca65', '3500f9ce-59be-45c1-9917-50bf0e232f4e', 28, 5);
    DELETE FROM llm968j_place p WHERE NOT EXISTS (SELECT 1 FROM llm968j_asset v WHERE v.asset_id = p.asset_id);

    -- Refuse before writing anything: a Wagon or Covered Wagon placement that
    -- is not the known one on its tile, the known one now on another asset,
    -- or the known one no longer a bare object (attached to something, or
    -- promoted to a structure: a structure shares its placement's id).
    SELECT count(*) INTO bad FROM village_object o
     WHERE o.asset_id IN (SELECT asset_id FROM llm968j_asset WHERE is_obstacle)
       AND NOT EXISTS (SELECT 1 FROM llm968j_place p
                        WHERE p.id = o.id AND p.asset_id = o.asset_id
                          AND p.tx = floor(o.x / 32) AND p.ty = floor(o.y / 32));
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968j: % wagon placement(s) not the checked covered wagon on its tile', bad;
    END IF;
    SELECT count(*) INTO bad FROM village_object o JOIN llm968j_place p ON p.id = o.id
     WHERE o.asset_id IS DISTINCT FROM p.asset_id;
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968j: the checked covered wagon is now on another asset';
    END IF;
    SELECT count(*) INTO bad FROM village_object o JOIN llm968j_place p ON p.id = o.id
     WHERE o.attached_to IS NOT NULL OR EXISTS (SELECT 1 FROM structure st WHERE st.id = o.id::text);
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968j: the checked covered wagon is attached or a structure, not a bare object';
    END IF;

    UPDATE asset a
       SET category = 'prop', render_scale = 1, anchor_x = v.anchor_x, anchor_y = v.anchor_y,
           footprint_left = v.fl, footprint_right = v.fr, footprint_top = v.ft, footprint_bottom = v.fb,
           door_offset_x = NULL, door_offset_y = NULL
      FROM llm968j_asset v
     WHERE a.id = v.asset_id;
    -- The existing default state only: the check below fails on a missing or
    -- extra one.
    UPDATE asset_state s
       SET sheet = v.sheet, src_x = v.src_x, src_y = 0, src_w = v.src_w, src_h = v.src_h,
           frame_count = 1, frame_rate = 0
      FROM llm968j_asset v
     WHERE s.asset_id = v.asset_id AND s.state = 'default';
    UPDATE village_object o
       SET x = p.tx * 32 + 16, y = p.ty * 32 + 16
      FROM llm968j_place p
     WHERE o.id = p.id AND o.asset_id = p.asset_id;

    SELECT count(*) INTO bad FROM (
        (SELECT asset_id, 'default'::text AS state, sheet, src_x, 0 AS src_y, src_w, src_h, 1 AS frame_count,
                0::double precision AS frame_rate FROM llm968j_asset
         EXCEPT
         SELECT s.asset_id, s.state::text, s.sheet::text, s.src_x, s.src_y, s.src_w, s.src_h, s.frame_count, s.frame_rate
           FROM asset_state s JOIN llm968j_asset v ON v.asset_id = s.asset_id)
        UNION ALL
        (SELECT s.asset_id, s.state::text, s.sheet::text, s.src_x, s.src_y, s.src_w, s.src_h, s.frame_count, s.frame_rate
           FROM asset_state s JOIN llm968j_asset v ON v.asset_id = s.asset_id
         EXCEPT
         SELECT asset_id, 'default'::text, sheet, src_x, 0, src_w, src_h, 1, 0::double precision FROM llm968j_asset)
    ) diff;
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968j: % wagon state rows differ from the authored values', bad;
    END IF;
    SELECT count(*) INTO bad FROM asset_state s JOIN llm968j_asset v ON v.asset_id = s.asset_id
     WHERE EXISTS (SELECT 1 FROM asset_state_tag t WHERE t.state_id = s.id)
        OR EXISTS (SELECT 1 FROM asset_state_light l WHERE l.state_id = s.id);
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968j: % wagon state(s) carry a tag or a light', bad;
    END IF;
    SELECT count(*) INTO bad FROM asset a JOIN llm968j_asset v ON v.asset_id = a.id
     WHERE (a.category::text, a.render_scale, a.anchor_x, a.anchor_y, a.footprint_left, a.footprint_right, a.footprint_top,
            a.footprint_bottom, a.door_offset_x, a.door_offset_y, a.is_obstacle, a.default_state::text)
           IS DISTINCT FROM ('prop'::text, 1::double precision, v.anchor_x, v.anchor_y, v.fl, v.fr, v.ft, v.fb,
                             NULL::integer, NULL::integer, v.is_obstacle, 'default'::text)
        OR EXISTS (SELECT 1 FROM asset_slot sl WHERE sl.asset_id = a.id);
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968j: % wagon asset(s) differ from the authored columns', bad;
    END IF;
    SELECT count(*) INTO bad FROM village_object o JOIN llm968j_place p ON p.id = o.id
     WHERE (o.asset_id, o.x, o.y, o.current_state::text)
           IS DISTINCT FROM (p.asset_id, (p.tx * 32 + 16)::double precision, (p.ty * 32 + 16)::double precision,
                             'default'::text);
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968j: the covered wagon is not at its tile centre in its default state';
    END IF;
END $$;

COMMIT;
