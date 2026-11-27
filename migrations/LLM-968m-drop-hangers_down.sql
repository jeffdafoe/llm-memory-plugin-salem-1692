-- LLM-968m down: bring back Hanging Sign and Hanging Banner with their one
-- state each, exactly as the production snapshot (2026-11-27) had them.
--
-- All or nothing, like the up. Where Outhouse 1 is here (only a database with
-- the village catalog has it; the hangers were the earlier LLM-968 steps'
-- sign of that catalog, and this drops them) the catalog must be either
-- migrated (neither hanger here; then both are restored) or not migrated
-- (both here, exactly; a rerun, nothing to do). Without Outhouse 1 neither
-- may be here, and nothing happens (a fresh database). The state rows get new
-- ids; nothing pointed at the old ones. After restoring, every row is checked
-- against the snapshot both ways.

BEGIN;

CREATE TEMP TABLE llm968m_asset (id uuid, name text, category text, default_state text, anchor_x double precision, anchor_y double precision, layer text, created_at timestamp, pack_id text, interior boolean, fits_slot text, source_file text, rotation_algo text, transition_spread_seconds integer, is_obstacle boolean, is_passage boolean, z_index integer, footprint_left integer, footprint_right integer, footprint_top integer, footprint_bottom integer, door_offset_x integer, door_offset_y integer, visible_when_inside boolean, stand_offset_x integer, stand_offset_y integer, occupied_min_count integer, occupied_night_only boolean, render_scale double precision) ON COMMIT DROP;
INSERT INTO llm968m_asset VALUES
    ('91cbb3a7-da0d-40f1-97d6-5fc04c0326b8', 'Hanging Banner', 'prop', 'default', 0.5, 0.5, 'above', '2026-04-11 18:50:27', 'mana-seed', false, NULL, NULL, 'random_per_object', 0, false, false, 10, 0, 0, 0, 0, NULL, NULL, false, NULL, NULL, 1, false, 2),
    ('01404aa0-8f3c-4e85-9d55-eb7099d6d89e', 'Hanging Sign', 'prop', 'default', 0.5, 0.5, 'above', '2026-04-11 18:50:27', 'mana-seed', false, NULL, NULL, 'random_per_object', 0, false, false, 10, 0, 0, 0, 0, NULL, NULL, false, NULL, NULL, 1, false, 2);

CREATE TEMP TABLE llm968m_state (asset_id uuid, state text, sheet text, src_x integer, src_y integer, src_w integer, src_h integer, frame_count integer, frame_rate double precision, created_at timestamp) ON COMMIT DROP;
INSERT INTO llm968m_state VALUES
    ('91cbb3a7-da0d-40f1-97d6-5fc04c0326b8', 'default', '/tilesets/mana-seed/village-accessories/village accessories 32x32.png', 96, 64, 32, 32, 1, 0, '2026-04-11 18:50:27'),  -- Hanging Banner
    ('01404aa0-8f3c-4e85-9d55-eb7099d6d89e', 'default', '/tilesets/mana-seed/village-accessories/village accessories 32x32.png', 64, 64, 32, 32, 1, 0, '2026-04-11 18:50:27');  -- Hanging Sign

CREATE TEMP TABLE llm968m_tag (asset_id uuid, state text, tag text) ON COMMIT DROP;
CREATE TEMP TABLE llm968m_light (asset_id uuid, state text, color text, radius integer, energy double precision, offset_x integer, offset_y integer, flicker_amplitude double precision, flicker_period_ms integer) ON COMMIT DROP;

CREATE TEMP VIEW llm968m_asset_now AS
SELECT id, name::text, category::text, default_state::text, anchor_x, anchor_y, layer::text, created_at, pack_id::text, interior, fits_slot::text, source_file::text, rotation_algo::text, transition_spread_seconds, is_obstacle, is_passage, z_index, footprint_left, footprint_right, footprint_top, footprint_bottom, door_offset_x, door_offset_y, visible_when_inside, stand_offset_x, stand_offset_y, occupied_min_count, occupied_night_only, render_scale FROM asset WHERE id IN (SELECT id FROM llm968m_asset);
CREATE TEMP VIEW llm968m_state_now AS
SELECT asset_id, state::text, sheet::text, src_x, src_y, src_w, src_h, frame_count, frame_rate, created_at FROM asset_state WHERE asset_id IN (SELECT id FROM llm968m_asset);
CREATE TEMP VIEW llm968m_tag_now AS
SELECT s.asset_id, s.state::text, t.tag::text
  FROM asset_state_tag t JOIN asset_state s ON s.id = t.state_id
 WHERE s.asset_id IN (SELECT id FROM llm968m_asset);
CREATE TEMP VIEW llm968m_light_now AS
SELECT s.asset_id, s.state::text, l.color::text, l.radius, l.energy, l.offset_x, l.offset_y,
       l.flicker_amplitude, l.flicker_period_ms
  FROM asset_state_light l JOIN asset_state s ON s.id = l.state_id
 WHERE s.asset_id IN (SELECT id FROM llm968m_asset);

LOCK TABLE asset, asset_state, asset_state_tag, asset_state_light, asset_slot, asset_refresh_default,
           village_object IN SHARE ROW EXCLUSIVE MODE;

DO $$
DECLARE
    outhouse CONSTANT uuid := '053ec0f2-79bd-4db6-a9df-fec642792244';
    bad integer;
    what text;
BEGIN
    IF NOT EXISTS (SELECT 1 FROM asset WHERE id = outhouse) THEN
        IF EXISTS (SELECT 1 FROM llm968m_asset_now) OR EXISTS (SELECT 1 FROM llm968m_state_now) THEN
            RAISE EXCEPTION 'LLM-968m down: hanger rows are here without the village catalog';
        END IF;
        RETURN;
    END IF;

    IF NOT EXISTS (SELECT 1 FROM llm968m_asset_now) THEN
        -- Migrated: restore both.
        IF EXISTS (SELECT 1 FROM llm968m_state_now) THEN
            RAISE EXCEPTION 'LLM-968m down: the catalog is partly migrated (hanger states are here without their assets)';
        END IF;
        INSERT INTO asset (id, name, category, default_state, anchor_x, anchor_y, layer, created_at, pack_id, interior, fits_slot, source_file, rotation_algo, transition_spread_seconds, is_obstacle, is_passage, z_index, footprint_left, footprint_right, footprint_top, footprint_bottom, door_offset_x, door_offset_y, visible_when_inside, stand_offset_x, stand_offset_y, occupied_min_count, occupied_night_only, render_scale) SELECT * FROM llm968m_asset;
        INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate, created_at) SELECT * FROM llm968m_state;
    END IF;

    -- Restored now, or a rerun: either way it must be the snapshot exactly.
    FOR what IN SELECT unnest(ARRAY['asset', 'state', 'tag', 'light']) LOOP
        EXECUTE format('SELECT count(*) FROM ((SELECT * FROM llm968m_%1$s EXCEPT ALL SELECT * FROM llm968m_%1$s_now)'
                       ' UNION ALL (SELECT * FROM llm968m_%1$s_now EXCEPT ALL SELECT * FROM llm968m_%1$s)) diff', what)
           INTO bad;
        IF bad > 0 THEN
            RAISE EXCEPTION 'LLM-968m down: % % row(s) differ from the snapshot', bad, what;
        END IF;
    END LOOP;
END $$;

COMMIT;
