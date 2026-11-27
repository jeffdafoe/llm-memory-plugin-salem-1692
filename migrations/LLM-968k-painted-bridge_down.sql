-- LLM-968 down: the Bridge back on Mana Seed summer-forest extras/bonus
-- bridge.png at render_scale 2 (anchor 0.5/0.7, the pack rect), its old
-- footprint 2,1,2,1 and door (0, -1). Each of the three bridges the up moved
-- goes back to its exact position from before the up, but only while it is
-- still where the up left it (its new tile's centre); one already back is
-- accepted (a rerun); one moved since makes the down refuse rather than
-- overwrite that edit.
--
-- The asset is set, then checked; any difference fails the migration.
-- Convergent.

BEGIN;

LOCK TABLE asset, asset_state, village_object IN SHARE ROW EXCLUSIVE MODE;

DO $$
DECLARE
    bad integer;
    bridge CONSTANT uuid := '35c7f051-266c-42a9-8650-f4f7ad4dc6a6';
    pack CONSTANT text := '/tilesets/mana-seed/summer-forest/extras/bonus bridge.png';
BEGIN
    IF NOT EXISTS (SELECT 1 FROM asset WHERE id = bridge) THEN
        RETURN;
    END IF;

    -- The three bridges: where the up left them (their new tile's centre)
    -- and where they were before.
    CREATE TEMP TABLE llm968k_down_place (id uuid PRIMARY KEY, nx integer, ny integer,
                                          x double precision, y double precision) ON COMMIT DROP;
    INSERT INTO llm968k_down_place VALUES
        ('019d98b9-fa41-7713-aa41-19700a7ab1be', 62, 6, 2015.90002441406, 220.351501464844),      -- A
        ('019d79ef-d9df-7ce4-a994-3abe29ec4dad', 63, 22, 2033.89990234375, 753.802124023438),     -- B
        ('019da672-2094-7a92-9cac-93fd3ee09cd9', 64, -27, 2083.84912109375, -832.535522460938);   -- C

    SELECT count(*) INTO bad FROM village_object o JOIN llm968k_down_place p ON p.id = o.id
     WHERE (o.asset_id, o.x, o.y)
           IS DISTINCT FROM (bridge, (p.nx * 32 + 16)::double precision, (p.ny * 32 + 16)::double precision)
       AND (o.asset_id, o.x, o.y) IS DISTINCT FROM (bridge, p.x, p.y);
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968k down: % of the three bridges moved or edited since the up; not overwriting', bad;
    END IF;

    UPDATE asset
       SET render_scale = 2, anchor_x = 0.5, anchor_y = 0.7,
           footprint_left = 2, footprint_right = 1, footprint_top = 2, footprint_bottom = 1,
           door_offset_x = 0, door_offset_y = -1
     WHERE id = bridge;
    UPDATE asset_state
       SET sheet = pack, src_x = 0, src_y = 0, src_w = 64, src_h = 48, frame_count = 1, frame_rate = 0
     WHERE asset_id = bridge AND state = 'default';
    UPDATE village_object o
       SET x = p.x, y = p.y
      FROM llm968k_down_place p
     WHERE o.id = p.id AND o.asset_id = bridge;

    IF (SELECT count(*) FROM asset_state WHERE asset_id = bridge) <> 1
       OR EXISTS (SELECT 1 FROM asset_state s WHERE s.asset_id = bridge
                   AND (s.state::text, s.sheet::text, s.src_x, s.src_y, s.src_w, s.src_h, s.frame_count, s.frame_rate)
                       IS DISTINCT FROM ('default'::text, pack, 0, 0, 64, 48, 1, 0::double precision)) THEN
        RAISE EXCEPTION 'LLM-968k down: the bridge state differs from the pack values';
    END IF;
    IF EXISTS (SELECT 1 FROM asset a WHERE a.id = bridge
                AND (a.render_scale, a.anchor_x, a.anchor_y, a.footprint_left, a.footprint_right, a.footprint_top,
                     a.footprint_bottom, a.door_offset_x, a.door_offset_y)
                    IS DISTINCT FROM (2::double precision, 0.5::double precision, 0.7::double precision,
                                      2, 1, 2, 1, 0, -1)) THEN
        RAISE EXCEPTION 'LLM-968k down: the bridge asset differs from the pack columns';
    END IF;
    SELECT count(*) INTO bad FROM llm968k_down_place p JOIN village_object o ON o.id = p.id
     WHERE (o.asset_id, o.x, o.y) IS DISTINCT FROM (bridge, p.x, p.y);
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968k down: % of the three bridges not back where they were', bad;
    END IF;
END $$;

COMMIT;
