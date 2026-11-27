-- LLM-968 down: the Debris heap back on the Mana Seed composite
-- public-works-debris-heap.png at render_scale 2, as LLM-675b set it: worn
-- at x 0, storm at x 64, each 64x40.
--
-- The asset, if it exists, is set, then its states are checked both ways,
-- with the same anchor, footprint, slot and default state the up requires
-- (the pack art stands on them too); any difference fails the migration.
-- Convergent.

BEGIN;

-- Hold off any other catalog writer (the editor) between the presence check
-- and the checks below.
LOCK TABLE asset, asset_state IN SHARE ROW EXCLUSIVE MODE;

DO $$
DECLARE
    bad integer;
    debris CONSTANT uuid := '019e5f00-c401-7a10-9e00-000000675001';
    pack CONSTANT text := '/tilesets/mana-seed/village-accessories/public-works-debris-heap.png';
BEGIN
    IF NOT EXISTS (SELECT 1 FROM asset WHERE id = debris) THEN
        RETURN;
    END IF;

    CREATE TEMP TABLE llm968q_down_state (asset_id uuid, state text, sheet text, src_x integer, src_y integer,
                                          src_w integer, src_h integer, frame_count integer,
                                          frame_rate double precision) ON COMMIT DROP;
    INSERT INTO llm968q_down_state VALUES
        (debris, 'worn', pack, 0, 0, 64, 40, 1, 0),
        (debris, 'storm', pack, 64, 0, 64, 40, 1, 0);

    UPDATE asset SET render_scale = 2 WHERE id = debris;
    -- Existing states only: the check below fails on a missing one.
    UPDATE asset_state s
       SET sheet = v.sheet, src_x = v.src_x, src_y = v.src_y, src_w = v.src_w, src_h = v.src_h,
           frame_count = v.frame_count, frame_rate = v.frame_rate
      FROM llm968q_down_state v
     WHERE s.asset_id = v.asset_id AND s.state = v.state;

    SELECT count(*) INTO bad FROM (
        (SELECT asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate FROM llm968q_down_state
         EXCEPT
         SELECT asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate
           FROM asset_state WHERE asset_id = debris)
        UNION ALL
        (SELECT asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate
           FROM asset_state WHERE asset_id = debris
         EXCEPT
         SELECT asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate FROM llm968q_down_state)
    ) diff;
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968q down: % debris state rows differ from the pack values', bad;
    END IF;
    SELECT count(*) INTO bad FROM asset a
     WHERE a.id = debris
       AND (a.render_scale, a.anchor_x, a.anchor_y, a.footprint_left, a.footprint_right, a.footprint_top,
            a.footprint_bottom, a.default_state::text, a.fits_slot::text)
           IS DISTINCT FROM (2::double precision, 0.5::double precision, 0.85::double precision, 0, 0, 0, 0,
                             'worn'::text, 'debris'::text);
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968q down: the debris heap is not back at render_scale 2 on its anchor, footprint and slot';
    END IF;
END $$;

COMMIT;
