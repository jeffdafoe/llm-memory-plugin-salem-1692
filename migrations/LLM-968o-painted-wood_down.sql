-- LLM-968 down: Wood Pile (Large) and the Wood Shelter back on the Mana Seed
-- village accessories 32x32.png rects at render_scale 2, as the pack catalog
-- set them.
--
-- Each asset that exists is set, then checked both ways, with the same
-- anchor and footprint the up requires (the pack art stands on them too);
-- any difference fails the migration. Convergent.

BEGIN;

-- Hold off any other catalog writer (the editor) between the presence check
-- and the checks below.
LOCK TABLE asset, asset_state IN SHARE ROW EXCLUSIVE MODE;

DO $$
DECLARE
    bad integer;
    pack CONSTANT text := '/tilesets/mana-seed/village-accessories/village accessories 32x32.png';
BEGIN
    CREATE TEMP TABLE llm968o_down_asset (asset_id uuid PRIMARY KEY, fl integer, fr integer, ft integer, fb integer)
        ON COMMIT DROP;
    INSERT INTO llm968o_down_asset VALUES
        ('74881601-00fd-4c07-898b-a9d3c155b4cc', 0, 0, 0, 0),   -- Wood Pile (Large)
        ('57229387-1773-4bf8-89f0-3dc0b090ed4d', 0, 0, 2, 0);   -- Wood Shelter
    DELETE FROM llm968o_down_asset v WHERE NOT EXISTS (SELECT 1 FROM asset WHERE id = v.asset_id);
    IF NOT EXISTS (SELECT 1 FROM llm968o_down_asset) THEN
        RETURN;
    END IF;

    CREATE TEMP TABLE llm968o_down_state (asset_id uuid, state text, sheet text, src_x integer, src_y integer,
                                          src_w integer, src_h integer, frame_count integer,
                                          frame_rate double precision) ON COMMIT DROP;
    INSERT INTO llm968o_down_state VALUES
        ('74881601-00fd-4c07-898b-a9d3c155b4cc', 'default', pack, 64, 0, 32, 32, 1, 0),
        ('57229387-1773-4bf8-89f0-3dc0b090ed4d', 'default', pack, 96, 0, 32, 32, 1, 0);
    DELETE FROM llm968o_down_state v WHERE NOT EXISTS (SELECT 1 FROM llm968o_down_asset l WHERE l.asset_id = v.asset_id);

    UPDATE asset a SET render_scale = 2 FROM llm968o_down_asset v WHERE a.id = v.asset_id;
    -- Existing states only: the check below fails on a missing one.
    UPDATE asset_state s
       SET sheet = v.sheet, src_x = v.src_x, src_y = v.src_y, src_w = v.src_w, src_h = v.src_h,
           frame_count = v.frame_count, frame_rate = v.frame_rate
      FROM llm968o_down_state v
     WHERE s.asset_id = v.asset_id AND s.state = v.state;

    SELECT count(*) INTO bad FROM (
        (SELECT asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate FROM llm968o_down_state
         EXCEPT
         SELECT s.asset_id, s.state, s.sheet, s.src_x, s.src_y, s.src_w, s.src_h, s.frame_count, s.frame_rate
           FROM asset_state s JOIN llm968o_down_asset v ON v.asset_id = s.asset_id)
        UNION ALL
        (SELECT s.asset_id, s.state, s.sheet, s.src_x, s.src_y, s.src_w, s.src_h, s.frame_count, s.frame_rate
           FROM asset_state s JOIN llm968o_down_asset v ON v.asset_id = s.asset_id
         EXCEPT
         SELECT asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate FROM llm968o_down_state)
    ) diff;
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968o down: % woodpile state rows differ from the pack values', bad;
    END IF;
    SELECT count(*) INTO bad FROM asset a JOIN llm968o_down_asset v ON v.asset_id = a.id
     WHERE (a.render_scale, a.anchor_x, a.anchor_y, a.footprint_left, a.footprint_right, a.footprint_top,
            a.footprint_bottom)
           IS DISTINCT FROM (2::double precision, 0.5::double precision, 0.85::double precision, v.fl, v.fr, v.ft, v.fb);
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968o down: % woodpile(s) not back at render_scale 2 on their anchor and footprint', bad;
    END IF;
END $$;

COMMIT;
