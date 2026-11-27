-- LLM-968: painted bridge.
--
-- The Bridge drew Mana Seed summer-forest extras/bonus bridge.png, whose
-- licence forbids use alongside AI. tools/tree-art/bridge.py now paints it:
-- client/assets/trees/bridge.png (one 192x128 frame), shown at render_scale
-- 1, a timber trestle bridge whose deck covers 5 x 2 tiles. Jeff approved the
-- look, the footprint and the moves on 2026-11-27.
--
-- ASSET (live-only: no migration creates it): anchor 48/96, 36/64 (the
-- middle tile of the deck's south row); footprint left 2, right 2, top 1,
-- bottom 0 (the deck exactly; was 2,1,2,1, which made water beside the road
-- walkable too); door cleared (it was (0, -1), a walkable tile inside an
-- already walkable passage footprint). Still a passage, not an obstacle,
-- z_index 1 (drawn under the villagers crossing it). One state, default.
--
-- PLACEMENTS: the three bridges, by id, each on the anchor tile it is on now,
-- move to the centre of the tile that puts the deck on their crossing's two
-- road rows, centred on the stream:
--   A (62, 6) -> (62, 6)    B (63, 23) -> (63, 22)    C (65, -27) -> (64, -27)
-- Checked against the live map with the engine's walk-grid rules before
-- writing this: at each crossing the road path from a tile west of the
-- stream to one east is the same route at the same cost before and after;
-- the only tiles that change are water beside the road that the old
-- footprint made walkable, which go back to water; nothing else stands on
-- the new decks. A passage is stamped walkable over anything under it, so
-- the up refuses if another object's footprint lies under a new deck. It
-- also refuses on any other Bridge placement, or a known one attached,
-- promoted to a structure, moved off its tile or onto another asset: only
-- these three crossings were checked. It refuses too if one of the three is
-- missing.
--
-- The asset is set, then checked exactly (columns, its one state, no tags,
-- lights or slots) along with the placements; any difference fails the
-- migration. ENGINE-OWNED data, read at boot; deploy.sh runs stop -> migrate
-- -> start. Convergent: a rerun ends in the same place and repairs a hand
-- edit.

BEGIN;

LOCK TABLE asset, asset_state, asset_state_tag, asset_state_light, asset_slot, village_object, structure
    IN SHARE ROW EXCLUSIVE MODE;

DO $$
DECLARE
    bad integer;
    bridge CONSTANT uuid := '35c7f051-266c-42a9-8650-f4f7ad4dc6a6';
BEGIN
    IF NOT EXISTS (SELECT 1 FROM asset WHERE id = bridge) THEN
        RETURN;
    END IF;

    -- The three bridges: the anchor tile each is on now, and the one it
    -- moves to (world px / 32, floored).
    CREATE TEMP TABLE llm968k_place (id uuid PRIMARY KEY, tx integer, ty integer, nx integer, ny integer) ON COMMIT DROP;
    INSERT INTO llm968k_place VALUES
        ('019d98b9-fa41-7713-aa41-19700a7ab1be', 62, 6, 62, 6),        -- A
        ('019d79ef-d9df-7ce4-a994-3abe29ec4dad', 63, 23, 63, 22),      -- B
        ('019da672-2094-7a92-9cac-93fd3ee09cd9', 65, -27, 64, -27);    -- C

    -- Refuse before writing anything: one of the three missing (the checks
    -- below, the deck guard among them, assume all three crossings get
    -- their bridge), a Bridge placement that is not one of the three on its
    -- tile (old or, on a rerun, new), one of the three on another asset, or
    -- one of the three not a bare object.
    SELECT count(*) INTO bad FROM llm968k_place p WHERE NOT EXISTS (SELECT 1 FROM village_object o WHERE o.id = p.id);
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968k: % of the three bridges missing', bad;
    END IF;
    SELECT count(*) INTO bad FROM village_object o
     WHERE o.asset_id = bridge
       AND NOT EXISTS (SELECT 1 FROM llm968k_place p
                        WHERE p.id = o.id
                          AND ((p.tx, p.ty) = (floor(o.x / 32), floor(o.y / 32))
                               OR (p.nx, p.ny) = (floor(o.x / 32), floor(o.y / 32))));
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968k: % bridge placement(s) not among the three checked bridges on their tiles', bad;
    END IF;
    SELECT count(*) INTO bad FROM village_object o JOIN llm968k_place p ON p.id = o.id
     WHERE o.asset_id IS DISTINCT FROM bridge;
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968k: % of the three bridges now on another asset', bad;
    END IF;
    SELECT count(*) INTO bad FROM village_object o JOIN llm968k_place p ON p.id = o.id
     WHERE o.attached_to IS NOT NULL OR EXISTS (SELECT 1 FROM structure st WHERE st.id = o.id::text);
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968k: % of the three bridges attached or a structure, not a bare object', bad;
    END IF;
    -- And another object's footprint under a new deck (the deck's tiles:
    -- columns nx-2..nx+2, rows ny-1..ny), which the passage would make
    -- walkable.
    SELECT count(*) INTO bad
      FROM llm968k_place p
      JOIN village_object o ON o.attached_to IS NULL AND o.asset_id IS DISTINCT FROM bridge
      JOIN asset oa ON oa.id = o.asset_id
     WHERE floor(o.x / 32)::int - oa.footprint_left <= p.nx + 2 AND floor(o.x / 32)::int + oa.footprint_right >= p.nx - 2
       AND floor(o.y / 32)::int - oa.footprint_top <= p.ny AND floor(o.y / 32)::int + oa.footprint_bottom >= p.ny - 1;
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968k: % object(s) stand under a new bridge deck', bad;
    END IF;

    UPDATE asset
       SET render_scale = 1, anchor_x = 48.0::double precision / 96, anchor_y = 36.0::double precision / 64,
           footprint_left = 2, footprint_right = 2, footprint_top = 1, footprint_bottom = 0,
           door_offset_x = NULL, door_offset_y = NULL
     WHERE id = bridge;
    -- The existing default state only: the check below fails on a missing or
    -- extra one.
    UPDATE asset_state
       SET sheet = 'res://assets/trees/bridge.png', src_x = 0, src_y = 0, src_w = 192, src_h = 128,
           frame_count = 1, frame_rate = 0
     WHERE asset_id = bridge AND state = 'default';
    UPDATE village_object o
       SET x = p.nx * 32 + 16, y = p.ny * 32 + 16
      FROM llm968k_place p
     WHERE o.id = p.id AND o.asset_id = bridge;

    SELECT count(*) INTO bad FROM asset_state s
     WHERE s.asset_id = bridge
       AND (s.state::text, s.sheet::text, s.src_x, s.src_y, s.src_w, s.src_h, s.frame_count, s.frame_rate)
           IS DISTINCT FROM ('default'::text, 'res://assets/trees/bridge.png'::text, 0, 0, 192, 128, 1,
                             0::double precision);
    IF bad > 0 OR (SELECT count(*) FROM asset_state WHERE asset_id = bridge) <> 1 THEN
        RAISE EXCEPTION 'LLM-968k: the bridge does not have exactly its one authored default state';
    END IF;
    SELECT count(*) INTO bad FROM asset_state s
     WHERE s.asset_id = bridge
       AND (EXISTS (SELECT 1 FROM asset_state_tag t WHERE t.state_id = s.id)
            OR EXISTS (SELECT 1 FROM asset_state_light l WHERE l.state_id = s.id));
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968k: the bridge state carries a tag or a light';
    END IF;
    IF EXISTS (SELECT 1 FROM asset a WHERE a.id = bridge
                AND ((a.category::text, a.render_scale, a.anchor_x, a.anchor_y, a.footprint_left, a.footprint_right,
                      a.footprint_top, a.footprint_bottom, a.door_offset_x, a.door_offset_y, a.is_obstacle,
                      a.is_passage, a.z_index, a.default_state::text)
                     IS DISTINCT FROM ('prop'::text, 1::double precision, 48.0::double precision / 96,
                                       36.0::double precision / 64, 2, 2, 1, 0, NULL::integer, NULL::integer,
                                       false, true, 1, 'default'::text)
                     OR EXISTS (SELECT 1 FROM asset_slot sl WHERE sl.asset_id = a.id))) THEN
        RAISE EXCEPTION 'LLM-968k: the bridge asset differs from the authored columns';
    END IF;
    SELECT count(*) INTO bad FROM llm968k_place p JOIN village_object o ON o.id = p.id
     WHERE (o.asset_id, o.x, o.y, o.current_state::text)
           IS DISTINCT FROM (bridge, (p.nx * 32 + 16)::double precision, (p.ny * 32 + 16)::double precision,
                             'default'::text);
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968k: % of the three bridges not at their new tile centre in their default state', bad;
    END IF;
END $$;

COMMIT;
