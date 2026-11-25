-- LLM-968 down: Well (Bucket) and Well (Roofed) back on the Mana Seed
-- village-accessories sheet at render_scale 2, as the pack catalog set them
-- (both damaged states on the pack's bare stone ring). Run it with the
-- client's well_bucket.gd reverted too: its places are in the art's pixels.
--
-- This is a reset to the pack values, not an undo of whatever the up found:
-- the up keeps no record of the values it overwrote.
--
-- Each asset that exists is set, then checked both ways; any difference fails
-- the migration. Convergent.

BEGIN;

-- Hold off any other catalog writer (the editor) between the presence check
-- and the checks below.
LOCK TABLE asset, asset_state, asset_state_tag IN SHARE ROW EXCLUSIVE MODE;

DO $$
DECLARE
    bad integer;
BEGIN
    CREATE TEMP TABLE llm968f_down_well (asset_id uuid PRIMARY KEY) ON COMMIT DROP;
    INSERT INTO llm968f_down_well VALUES
        ('afc2110d-e882-461b-91c5-abb363a76476'),   -- Well (Bucket)
        ('64b37a3f-8b77-4083-bee9-37fe197b8270');   -- Well (Roofed)
    DELETE FROM llm968f_down_well v WHERE NOT EXISTS (SELECT 1 FROM asset WHERE id = v.asset_id);
    IF NOT EXISTS (SELECT 1 FROM llm968f_down_well) THEN
        RETURN;
    END IF;

    -- Every state, as the pack catalog had it.
    CREATE TEMP TABLE llm968f_down_state (asset_id uuid, state text, sheet text, src_x integer, src_y integer,
                                     src_w integer, src_h integer, frame_count integer,
                                     frame_rate double precision) ON COMMIT DROP;
    INSERT INTO llm968f_down_state VALUES
        ('afc2110d-e882-461b-91c5-abb363a76476', 'default', '/tilesets/mana-seed/village-accessories/village accessories 48x80.png', 96, 0, 48, 80, 1, 0),
        ('afc2110d-e882-461b-91c5-abb363a76476', 'damaged', '/tilesets/mana-seed/village-accessories/village accessories 48x80.png', 0, 0, 48, 80, 1, 0),
        ('64b37a3f-8b77-4083-bee9-37fe197b8270', 'default', '/tilesets/mana-seed/village-accessories/village accessories 48x80.png', 144, 0, 48, 80, 1, 0),
        ('64b37a3f-8b77-4083-bee9-37fe197b8270', 'damaged', '/tilesets/mana-seed/village-accessories/village accessories 48x80.png', 0, 0, 48, 80, 1, 0);
    DELETE FROM llm968f_down_state v WHERE NOT EXISTS (SELECT 1 FROM llm968f_down_well w WHERE w.asset_id = v.asset_id);

    -- Their tags, unchanged by this migration: the storm's damage finds a
    -- well's broken look by its damaged tag.
    CREATE TEMP TABLE llm968f_down_tag (asset_id uuid, state text, tag text) ON COMMIT DROP;
    INSERT INTO llm968f_down_tag
    SELECT a, s, t FROM (VALUES
        ('afc2110d-e882-461b-91c5-abb363a76476'::uuid, 'damaged', 'damaged'),
        ('64b37a3f-8b77-4083-bee9-37fe197b8270'::uuid, 'damaged', 'damaged')) v(a, s, t)
     WHERE EXISTS (SELECT 1 FROM llm968f_down_well w WHERE w.asset_id = v.a);

    UPDATE asset a SET render_scale = 2 FROM llm968f_down_well v WHERE a.id = v.asset_id;
    -- Existing states only: the check below fails on a missing one.
    UPDATE asset_state s
       SET sheet = v.sheet, src_x = v.src_x, src_y = v.src_y, src_w = v.src_w, src_h = v.src_h,
           frame_count = v.frame_count, frame_rate = v.frame_rate
      FROM llm968f_down_state v
     WHERE s.asset_id = v.asset_id AND s.state = v.state;

    SELECT count(*) INTO bad FROM (
        (SELECT asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate FROM llm968f_down_state
         EXCEPT
         SELECT s.asset_id, s.state, s.sheet, s.src_x, s.src_y, s.src_w, s.src_h, s.frame_count, s.frame_rate
           FROM asset_state s JOIN llm968f_down_well v ON v.asset_id = s.asset_id)
        UNION ALL
        (SELECT s.asset_id, s.state, s.sheet, s.src_x, s.src_y, s.src_w, s.src_h, s.frame_count, s.frame_rate
           FROM asset_state s JOIN llm968f_down_well v ON v.asset_id = s.asset_id
         EXCEPT
         SELECT asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate FROM llm968f_down_state)
    ) diff;
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968f down: % well state rows differ from the pack values', bad;
    END IF;
    SELECT count(*) INTO bad FROM (
        (SELECT asset_id, state, tag FROM llm968f_down_tag
         EXCEPT
         SELECT s.asset_id, s.state::text, t.tag::text
           FROM asset_state s JOIN llm968f_down_well v ON v.asset_id = s.asset_id JOIN asset_state_tag t ON t.state_id = s.id)
        UNION ALL
        (SELECT s.asset_id, s.state::text, t.tag::text
           FROM asset_state s JOIN llm968f_down_well v ON v.asset_id = s.asset_id JOIN asset_state_tag t ON t.state_id = s.id
         EXCEPT
         SELECT asset_id, state, tag FROM llm968f_down_tag)
    ) diff;
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968f down: % well state tags differ from the expected ones', bad;
    END IF;
    SELECT count(*) INTO bad FROM asset a JOIN llm968f_down_well v ON v.asset_id = a.id WHERE a.render_scale <> 2;
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968f down: % well(s) not back at render_scale 2', bad;
    END IF;
END $$;

COMMIT;
