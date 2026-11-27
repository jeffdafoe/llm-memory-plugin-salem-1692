-- LLM-968m: drop Hanging Sign and Hanging Banner, the two wall-bracket
-- hangers, instead of repainting them.
--
-- Jeff (2026-11-27): drop them. LLM-968 kept them only because the Shop Signs
-- hung from their 'sign' slot; that step removed the Shop Signs and the slot.
-- Now neither is placed (village_object has no foreign key to asset, so a
-- placement would be left pointing at nothing), neither hosts a slot or has a
-- refresh default, and the engine and client name neither. The editor palette
-- is built from the catalog, so they leave it.
--
-- WHAT THIS DOES: deletes the two assets and their one state each (no tags,
-- no lights).
--
-- All or nothing, so the down reverses it exactly. The catalog must be either
-- exactly the production snapshot (2026-11-27) for these rows (compared both
-- ways, typed, with the state tags and lights) with Outhouse 1 here (the
-- down's sign of the village catalog), or already migrated (neither
-- here; a fresh database, which has no catalog, is this case and nothing
-- happens). Anything in between fails the migration rather than lose a row
-- the down cannot bring back.
--
-- ENGINE-OWNED data (the catalog), read at boot; deploy.sh runs stop ->
-- migrate -> start. Rerunnable: a second run finds the catalog migrated.

BEGIN;

CREATE TEMP TABLE llm968m_asset (id uuid, name text, category text, default_state text, anchor_x double precision, anchor_y double precision, layer text, created_at timestamp, pack_id text, interior boolean, fits_slot text, source_file text, rotation_algo text, transition_spread_seconds integer, is_obstacle boolean, is_passage boolean, z_index integer, footprint_left integer, footprint_right integer, footprint_top integer, footprint_bottom integer, door_offset_x integer, door_offset_y integer, visible_when_inside boolean, stand_offset_x integer, stand_offset_y integer, occupied_min_count integer, occupied_night_only boolean, render_scale double precision) ON COMMIT DROP;
INSERT INTO llm968m_asset VALUES
    ('91cbb3a7-da0d-40f1-97d6-5fc04c0326b8', 'Hanging Banner', 'prop', 'default', 0.5, 0.5, 'above', '2026-04-11 18:50:27', 'mana-seed', false, NULL, NULL, 'random_per_object', 0, false, false, 10, 0, 0, 0, 0, NULL, NULL, false, NULL, NULL, 1, false, 2),
    ('01404aa0-8f3c-4e85-9d55-eb7099d6d89e', 'Hanging Sign', 'prop', 'default', 0.5, 0.5, 'above', '2026-04-11 18:50:27', 'mana-seed', false, NULL, NULL, 'random_per_object', 0, false, false, 10, 0, 0, 0, 0, NULL, NULL, false, NULL, NULL, 1, false, 2);

CREATE TEMP TABLE llm968m_state (asset_id uuid, state text, sheet text, src_x integer, src_y integer, src_w integer, src_h integer, frame_count integer, frame_rate double precision, created_at timestamp) ON COMMIT DROP;
INSERT INTO llm968m_state VALUES
    ('91cbb3a7-da0d-40f1-97d6-5fc04c0326b8', 'default', '/tilesets/mana-seed/village-accessories/village accessories 32x32.png', 96, 64, 32, 32, 1, 0, '2026-04-11 18:50:27'),  -- Hanging Banner
    ('01404aa0-8f3c-4e85-9d55-eb7099d6d89e', 'default', '/tilesets/mana-seed/village-accessories/village accessories 32x32.png', 64, 64, 32, 32, 1, 0, '2026-04-11 18:50:27');  -- Hanging Sign

-- Neither state has tags or lights; the empty snapshots make the compares
-- below fail if any appear.
CREATE TEMP TABLE llm968m_tag (asset_id uuid, state text, tag text) ON COMMIT DROP;
CREATE TEMP TABLE llm968m_light (asset_id uuid, state text, color text, radius integer, energy double precision, offset_x integer, offset_y integer, flicker_amplitude double precision, flicker_period_ms integer) ON COMMIT DROP;

-- The catalog as it is now, in the snapshot's shapes, typed, for the
-- both-ways compares.
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

-- Only the engine writes the catalog and the placements, and deploy.sh stops
-- it before migrating; the lock keeps any other writer out between the checks
-- and the changes.
LOCK TABLE asset, asset_state, asset_state_tag, asset_state_light, asset_slot, asset_refresh_default,
           village_object IN SHARE ROW EXCLUSIVE MODE;

DO $$
DECLARE
    outhouse CONSTANT uuid := '053ec0f2-79bd-4db6-a9df-fec642792244';
    bad integer;
    what text;
BEGIN
    -- Nothing may point at an asset that goes, nor at one already gone (a
    -- rerun): there are no foreign keys to catch it.
    SELECT count(*) INTO bad FROM village_object WHERE asset_id IN (SELECT id FROM llm968m_asset);
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968m: % placement(s) of a hanger; remove them (engine stopped) first', bad;
    END IF;
    SELECT count(*) INTO bad FROM asset_slot WHERE asset_id IN (SELECT id FROM llm968m_asset);
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968m: % slot(s) on a hanger', bad;
    END IF;
    SELECT count(*) INTO bad FROM asset_refresh_default WHERE asset_id IN (SELECT id FROM llm968m_asset);
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968m: % refresh default(s) on a hanger', bad;
    END IF;

    -- Already migrated (or a fresh database): none of it may be here.
    IF NOT EXISTS (SELECT 1 FROM llm968m_asset_now) THEN
        IF EXISTS (SELECT 1 FROM llm968m_state_now) THEN
            RAISE EXCEPTION 'LLM-968m: the catalog is partly migrated (hanger states are here without their assets)';
        END IF;
        RETURN;
    END IF;
    -- The down restores only where Outhouse 1 shows the village catalog is
    -- here; without it the down could not bring these rows back.
    IF NOT EXISTS (SELECT 1 FROM asset WHERE id = outhouse) THEN
        RAISE EXCEPTION 'LLM-968m: Outhouse 1 is not here, so the down could not bring the hangers back';
    END IF;

    FOR what IN SELECT unnest(ARRAY['asset', 'state', 'tag', 'light']) LOOP
        EXECUTE format('SELECT count(*) FROM ((SELECT * FROM llm968m_%1$s EXCEPT ALL SELECT * FROM llm968m_%1$s_now)'
                       ' UNION ALL (SELECT * FROM llm968m_%1$s_now EXCEPT ALL SELECT * FROM llm968m_%1$s)) diff', what)
           INTO bad;
        IF bad > 0 THEN
            RAISE EXCEPTION 'LLM-968m: % % row(s) differ from the snapshot', bad, what;
        END IF;
    END LOOP;

    -- Tags and lights go with their states (ON DELETE CASCADE); there are none.
    DELETE FROM asset_state WHERE asset_id IN (SELECT id FROM llm968m_asset);
    DELETE FROM asset WHERE id IN (SELECT id FROM llm968m_asset);
END $$;

COMMIT;
