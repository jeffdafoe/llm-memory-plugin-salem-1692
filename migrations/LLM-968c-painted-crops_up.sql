-- LLM-968: painted field crops: Wheat and Carrots.
--
-- They drew the Mana Seed farming-crops sheets, whose licence forbids use
-- alongside AI. tools/tree-art/crops.py now paints them:
-- client/assets/trees/wheat.png and carrot.png.
--
-- Sheet layout (crops.py): one row of six 32x64 still frames, left to right
-- growth-1 .. growth-6. That is twice the pack art's 16x32, drawn at
-- render_scale 1 instead of 2: the same size in the world with twice the
-- detail, as the ground cover (LLM-967l). The anchor stays 0.5/0.85, so every
-- placed plant keeps its place.
--
-- No state is added or removed and no tag changes: state growth-N (tagged
-- growth-N, which the engine's cropStageState orders by) points at frame N.
-- No placement moves.
--
-- For each crop that exists (both are created by LLM-576 and LLM-603, so a
-- fresh database has them too), its render_scale becomes 1 and its states
-- point at its sheet; then each crop must have exactly these six states with
-- exactly these values, each carrying exactly its growth-N tag, or the
-- migration fails. A missing state is not recreated (it would come back
-- without its tag); the check fails on it instead.
--
-- ENGINE-OWNED data (the catalog), read at boot; deploy.sh runs stop ->
-- migrate -> start. Convergent: rerunning ends in the same place, and a rerun
-- repairs a hand edit of a state's rect.

BEGIN;

DO $$
DECLARE
    bad integer;
BEGIN
    CREATE TEMP TABLE llm968c_crop (asset_id uuid PRIMARY KEY, sheet text) ON COMMIT DROP;
    INSERT INTO llm968c_crop VALUES
        ('019e5f00-c401-7a10-9e00-000000000576', 'res://assets/trees/wheat.png'),   -- Wheat
        ('019e5f00-c401-7a10-9e00-000000000603', 'res://assets/trees/carrot.png');  -- Carrots
    -- A crop that is not in this database takes no part.
    DELETE FROM llm968c_crop v WHERE NOT EXISTS (SELECT 1 FROM asset WHERE id = v.asset_id);
    IF NOT EXISTS (SELECT 1 FROM llm968c_crop) THEN
        RETURN;
    END IF;

    UPDATE asset a SET render_scale = 1 FROM llm968c_crop v WHERE a.id = v.asset_id;

    -- Every state of every crop, as authored: growth-N shows frame N.
    CREATE TEMP TABLE llm968c_state ON COMMIT DROP AS
    SELECT v.asset_id, 'growth-' || n AS state, v.sheet, (n - 1) * 32 AS src_x, 0 AS src_y, 32 AS src_w, 64 AS src_h,
           1 AS frame_count, 0::double precision AS frame_rate
      FROM llm968c_crop v CROSS JOIN generate_series(1, 6) n;

    -- Existing states only: the check below fails on a missing one.
    UPDATE asset_state s
       SET sheet = v.sheet, src_x = v.src_x, src_y = v.src_y, src_w = v.src_w, src_h = v.src_h,
           frame_count = v.frame_count, frame_rate = v.frame_rate
      FROM llm968c_state v
     WHERE s.asset_id = v.asset_id AND s.state = v.state;

    -- Every crop has exactly its states, with exactly these values.
    SELECT count(*) INTO bad FROM (
        (SELECT asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate FROM llm968c_state
         EXCEPT
         SELECT s.asset_id, s.state, s.sheet, s.src_x, s.src_y, s.src_w, s.src_h, s.frame_count, s.frame_rate
           FROM asset_state s JOIN llm968c_crop v ON v.asset_id = s.asset_id)
        UNION ALL
        (SELECT s.asset_id, s.state, s.sheet, s.src_x, s.src_y, s.src_w, s.src_h, s.frame_count, s.frame_rate
           FROM asset_state s JOIN llm968c_crop v ON v.asset_id = s.asset_id
         EXCEPT
         SELECT asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate FROM llm968c_state)
    ) diff;
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968c: % crop state rows differ from the authored values', bad;
    END IF;
    -- And each state carries exactly its own growth-N tag: the engine's stage
    -- order (cropStageState) and ripeness come from these tags.
    SELECT count(*) INTO bad FROM (
        (SELECT asset_id, state, state AS tag FROM llm968c_state
         EXCEPT
         SELECT s.asset_id, s.state::text, t.tag::text
           FROM asset_state s JOIN llm968c_crop v ON v.asset_id = s.asset_id JOIN asset_state_tag t ON t.state_id = s.id)
        UNION ALL
        (SELECT s.asset_id, s.state::text, t.tag::text
           FROM asset_state s JOIN llm968c_crop v ON v.asset_id = s.asset_id JOIN asset_state_tag t ON t.state_id = s.id
         EXCEPT
         SELECT asset_id, state, state FROM llm968c_state)
    ) diff;
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968c: % crop state tags differ from growth-N on state growth-N', bad;
    END IF;
    SELECT count(*) INTO bad FROM asset a JOIN llm968c_crop v ON v.asset_id = a.id WHERE a.render_scale <> 1;
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968c: % crop(s) not at render_scale 1', bad;
    END IF;
END $$;

COMMIT;
