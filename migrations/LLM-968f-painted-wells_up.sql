-- LLM-968: painted wells: Well (Bucket) and Well (Roofed).
--
-- They drew Mana Seed village-accessories art, whose licence forbids use
-- alongside AI. tools/tree-art/wells.py now paints them: client/assets/trees/
-- well-bucket.png and well-roofed.png, the default look then the damaged
-- look, 96x160 frames: twice the pack art's size, shown at render_scale 1 (the
-- same size in the world, twice the detail). Anchors and footprints do not
-- change, so the placed wells keep their places, and the shaft is where the
-- pack well's was: client/scripts/well_bucket.gd (in this same change) puts
-- the pail and the drinker's kneel on the same world spots, in the painted
-- sheets' pixels, and draws the painted pail (well-pail.png) packed with the
-- client.
--
-- Each well's damaged state (the storm's broken windlass, damage_minor) now
-- shows its own broken frame: before, both pointed at the pack's bare stone
-- ring. No state, tag, refresh default or placement changes; the damaged
-- states keep their damaged tag, checked here.
--
-- Each asset that exists is set, then its states and tags are checked both
-- ways against these values; any difference fails the migration. A missing
-- state is not recreated (it would come back without its tags); the check
-- fails on it. An asset not in this database takes no part (neither well is
-- created by a migration).
--
-- ENGINE-OWNED data (the catalog), read at boot; deploy.sh runs stop ->
-- migrate -> start. Convergent: a rerun ends in the same place and repairs a
-- hand edit of a rect (the up writes the authored values over whatever is
-- there; it does not keep them to restore).

BEGIN;

-- Hold off any other catalog writer (the editor) between the presence check
-- and the checks below.
LOCK TABLE asset, asset_state, asset_state_tag IN SHARE ROW EXCLUSIVE MODE;

DO $$
DECLARE
    bad integer;
BEGIN
    CREATE TEMP TABLE llm968f_well (asset_id uuid PRIMARY KEY) ON COMMIT DROP;
    INSERT INTO llm968f_well VALUES
        ('afc2110d-e882-461b-91c5-abb363a76476'),   -- Well (Bucket)
        ('64b37a3f-8b77-4083-bee9-37fe197b8270');   -- Well (Roofed)
    DELETE FROM llm968f_well v WHERE NOT EXISTS (SELECT 1 FROM asset WHERE id = v.asset_id);
    IF NOT EXISTS (SELECT 1 FROM llm968f_well) THEN
        RETURN;
    END IF;

    -- Every state, as authored.
    CREATE TEMP TABLE llm968f_state (asset_id uuid, state text, sheet text, src_x integer, src_y integer,
                                     src_w integer, src_h integer, frame_count integer,
                                     frame_rate double precision) ON COMMIT DROP;
    INSERT INTO llm968f_state VALUES
        ('afc2110d-e882-461b-91c5-abb363a76476', 'default', 'res://assets/trees/well-bucket.png', 0, 0, 96, 160, 1, 0),
        ('afc2110d-e882-461b-91c5-abb363a76476', 'damaged', 'res://assets/trees/well-bucket.png', 96, 0, 96, 160, 1, 0),
        ('64b37a3f-8b77-4083-bee9-37fe197b8270', 'default', 'res://assets/trees/well-roofed.png', 0, 0, 96, 160, 1, 0),
        ('64b37a3f-8b77-4083-bee9-37fe197b8270', 'damaged', 'res://assets/trees/well-roofed.png', 96, 0, 96, 160, 1, 0);
    DELETE FROM llm968f_state v WHERE NOT EXISTS (SELECT 1 FROM llm968f_well w WHERE w.asset_id = v.asset_id);

    -- Their tags, unchanged by this migration: the storm's damage finds a
    -- well's broken look by its damaged tag.
    CREATE TEMP TABLE llm968f_tag (asset_id uuid, state text, tag text) ON COMMIT DROP;
    INSERT INTO llm968f_tag
    SELECT a, s, t FROM (VALUES
        ('afc2110d-e882-461b-91c5-abb363a76476'::uuid, 'damaged', 'damaged'),
        ('64b37a3f-8b77-4083-bee9-37fe197b8270'::uuid, 'damaged', 'damaged')) v(a, s, t)
     WHERE EXISTS (SELECT 1 FROM llm968f_well w WHERE w.asset_id = v.a);

    UPDATE asset a SET render_scale = 1 FROM llm968f_well v WHERE a.id = v.asset_id;
    -- Existing states only: the check below fails on a missing one.
    UPDATE asset_state s
       SET sheet = v.sheet, src_x = v.src_x, src_y = v.src_y, src_w = v.src_w, src_h = v.src_h,
           frame_count = v.frame_count, frame_rate = v.frame_rate
      FROM llm968f_state v
     WHERE s.asset_id = v.asset_id AND s.state = v.state;

    SELECT count(*) INTO bad FROM (
        (SELECT asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate FROM llm968f_state
         EXCEPT
         SELECT s.asset_id, s.state, s.sheet, s.src_x, s.src_y, s.src_w, s.src_h, s.frame_count, s.frame_rate
           FROM asset_state s JOIN llm968f_well v ON v.asset_id = s.asset_id)
        UNION ALL
        (SELECT s.asset_id, s.state, s.sheet, s.src_x, s.src_y, s.src_w, s.src_h, s.frame_count, s.frame_rate
           FROM asset_state s JOIN llm968f_well v ON v.asset_id = s.asset_id
         EXCEPT
         SELECT asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate FROM llm968f_state)
    ) diff;
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968f: % well state rows differ from the authored values', bad;
    END IF;
    SELECT count(*) INTO bad FROM (
        (SELECT asset_id, state, tag FROM llm968f_tag
         EXCEPT
         SELECT s.asset_id, s.state::text, t.tag::text
           FROM asset_state s JOIN llm968f_well v ON v.asset_id = s.asset_id JOIN asset_state_tag t ON t.state_id = s.id)
        UNION ALL
        (SELECT s.asset_id, s.state::text, t.tag::text
           FROM asset_state s JOIN llm968f_well v ON v.asset_id = s.asset_id JOIN asset_state_tag t ON t.state_id = s.id
         EXCEPT
         SELECT asset_id, state, tag FROM llm968f_tag)
    ) diff;
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968f: % well state tags differ from the expected ones', bad;
    END IF;
    SELECT count(*) INTO bad FROM asset a JOIN llm968f_well v ON v.asset_id = a.id WHERE a.render_scale <> 1;
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968f: % well(s) not at render_scale 1', bad;
    END IF;
END $$;

COMMIT;
