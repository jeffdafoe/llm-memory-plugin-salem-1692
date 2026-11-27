-- LLM-968: the painted camp: the player's pitched camp (Tent, Camp Fire, Camp
-- Stool, Camp Woodpile, LLM-798) and the scenery set (Tent (Open), Tent
-- (Closed), Campfire, Bedroll, Cooking Spit, Camp Tripod, Camp Sack, Camp
-- Firewood).
--
-- They drew Mana Seed travelers-camp art, whose licence forbids use alongside
-- AI. tools/tree-art/camp.py now paints them, at twice the pack art's size and
-- shown at render_scale 1 (the same size in the world, twice the detail):
--   tent.png        closed, open, packed (128x128 frames)
--   camp-fire.png   unlit, burning x4, dying x4, ashes (64x64)
--   camp-small.png  stool, woodpile, sack (32x32)
--   camp-gear.png   bedroll rolled, bedroll laid out, spit with a fowl,
--                   bare spit, tripod with kettle (64x64)
-- Anchors stay 0.5/0.85, so every placement keeps its place. The Campfire's
-- burning state is now 4 frames (the pack's 5 began with the unlit logs) and
-- its unlit state one still frame; the Camp Fire's states keep their counts
-- and rates.
--
-- THE TENT'S FOOTPRINT shrinks to the painted tent: left 1, right 1, top 2,
-- bottom 0 (3 x 3; was 1, 2, 3, 0, 4 x 4). Its door stays the anchor tile
-- (0, 0), so the camp layout (engine/sim/camp.go campLayout: the stand tile in
-- front of the door, the fire, stool and woodpile at fixed offsets) and the
-- tent's open/closed occupancy are unchanged; the new footprint lies inside
-- the old one, so a pitched camp blocks no tile it did not before. The two
-- scenery tents take the same footprint and door (0, 0) (they were 1,2,3,0
-- with doors fitted to the pack tent, and are not placed).
--
-- LIGHTS (Jeff, 2026-11-25): the pitched Camp Fire now lights its camp. Burning
-- takes the scenery Campfire's light (#FF8833, radius 128, energy 1, flicker
-- 0.15 every 180 ms); dying a small ember glow (#D9552A, radius 56, energy
-- 0.7, flicker 0.08 every 400 ms); unlit and ashes stay dark. The Campfire's
-- own light is unchanged. Every light on the twelve assets is checked.
--
-- Each asset that exists is set, then its states, tags and lights are checked
-- both ways against these values; any difference fails the migration. A
-- missing state is not recreated (it would come back without its tags). The
-- four pitched-camp assets come from LLM-798, so a fresh database has them;
-- the scenery set is live-only.
--
-- ENGINE-OWNED data (the catalog), read at boot; deploy.sh runs stop ->
-- migrate -> start. Convergent: a rerun ends in the same place and repairs a
-- hand edit (the up writes the authored values over whatever is there; it
-- keeps nothing to restore).

BEGIN;

LOCK TABLE asset, asset_state, asset_state_tag, asset_state_light IN SHARE ROW EXCLUSIVE MODE;

DO $$
DECLARE
    bad integer;
BEGIN
    -- Every state, as authored: asset, state, sheet, x, w, h, frames, rate.
    CREATE TEMP TABLE llm968i_state (asset_id uuid, state text, sheet text, src_x integer, src_y integer, src_w integer,
                                     src_h integer, frame_count integer, frame_rate double precision) ON COMMIT DROP;
    INSERT INTO llm968i_state VALUES
        -- Tent (the player's)
        ('019e5f00-c401-7a10-9e00-000000798001', 'closed', 'res://assets/trees/tent.png', 0, 0, 128, 128, 1, 0),
        ('019e5f00-c401-7a10-9e00-000000798001', 'open', 'res://assets/trees/tent.png', 128, 0, 128, 128, 1, 0),
        ('019e5f00-c401-7a10-9e00-000000798001', 'packed', 'res://assets/trees/tent.png', 256, 0, 128, 128, 1, 0),
        -- Camp Fire
        ('019e5f00-c401-7a10-9e00-000000798002', 'unlit', 'res://assets/trees/camp-fire.png', 0, 0, 64, 64, 1, 0),
        ('019e5f00-c401-7a10-9e00-000000798002', 'burning', 'res://assets/trees/camp-fire.png', 64, 0, 64, 64, 4, 8),
        ('019e5f00-c401-7a10-9e00-000000798002', 'dying', 'res://assets/trees/camp-fire.png', 320, 0, 64, 64, 4, 6),
        ('019e5f00-c401-7a10-9e00-000000798002', 'ashes', 'res://assets/trees/camp-fire.png', 576, 0, 64, 64, 1, 0),
        -- Camp Stool, Camp Woodpile
        ('019e5f00-c401-7a10-9e00-000000798003', 'default', 'res://assets/trees/camp-small.png', 0, 0, 32, 32, 1, 0),
        ('019e5f00-c401-7a10-9e00-000000798004', 'default', 'res://assets/trees/camp-small.png', 32, 0, 32, 32, 1, 0),
        -- Tent (Closed), Tent (Open)
        ('18118703-3aa8-4f28-8ffd-3d3c9f0804ed', 'default', 'res://assets/trees/tent.png', 0, 0, 128, 128, 1, 0),
        ('a6f9d413-385f-43e6-9d19-b91f49bc343c', 'default', 'res://assets/trees/tent.png', 128, 0, 128, 128, 1, 0),
        -- Campfire
        ('3b42aadd-dc7e-411c-90cc-f3d8395a7c32', 'default', 'res://assets/trees/camp-fire.png', 64, 0, 64, 64, 4, 8),
        ('3b42aadd-dc7e-411c-90cc-f3d8395a7c32', 'unlit', 'res://assets/trees/camp-fire.png', 0, 0, 64, 64, 1, 0),
        -- Camp Firewood, Camp Sack
        ('95021559-d659-476f-888e-cc0692f89cba', 'default', 'res://assets/trees/camp-small.png', 32, 0, 32, 32, 1, 0),
        ('daa827fd-9cd8-4958-98e3-aed4953b9ec8', 'default', 'res://assets/trees/camp-small.png', 64, 0, 32, 32, 1, 0),
        -- Bedroll, Cooking Spit, Camp Tripod
        ('3d079da4-2e38-42dc-abc4-583254bf197a', 'closed', 'res://assets/trees/camp-gear.png', 0, 0, 64, 64, 1, 0),
        ('3d079da4-2e38-42dc-abc4-583254bf197a', 'open', 'res://assets/trees/camp-gear.png', 64, 0, 64, 64, 1, 0),
        ('74ec990c-9710-4444-9817-7bfccdb200f7', 'default', 'res://assets/trees/camp-gear.png', 128, 0, 64, 64, 1, 0),
        ('74ec990c-9710-4444-9817-7bfccdb200f7', 'empty', 'res://assets/trees/camp-gear.png', 192, 0, 64, 64, 1, 0),
        ('562b3d35-6164-41dd-8ed5-0c66dd837b19', 'default', 'res://assets/trees/camp-gear.png', 256, 0, 64, 64, 1, 0);
    -- An asset not in this database takes no part.
    DELETE FROM llm968i_state v WHERE NOT EXISTS (SELECT 1 FROM asset WHERE id = v.asset_id);
    IF NOT EXISTS (SELECT 1 FROM llm968i_state) THEN
        RETURN;
    END IF;
    CREATE TEMP TABLE llm968i_asset ON COMMIT DROP AS SELECT DISTINCT asset_id FROM llm968i_state;

    -- Their tags, unchanged by this migration.
    CREATE TEMP TABLE llm968i_tag (asset_id uuid, state text, tag text) ON COMMIT DROP;
    INSERT INTO llm968i_tag
    SELECT a, s, t FROM (VALUES
        ('019e5f00-c401-7a10-9e00-000000798001'::uuid, 'closed', 'unoccupied'),
        ('019e5f00-c401-7a10-9e00-000000798001'::uuid, 'open', 'occupied'),
        ('3b42aadd-dc7e-411c-90cc-f3d8395a7c32'::uuid, 'default', 'night-active'),
        ('3b42aadd-dc7e-411c-90cc-f3d8395a7c32'::uuid, 'unlit', 'day-active')) v(a, s, t)
     WHERE EXISTS (SELECT 1 FROM llm968i_asset x WHERE x.asset_id = v.a);

    -- Every light on the twelve: the Campfire's, as it is, and the Camp
    -- Fire's two new ones.
    CREATE TEMP TABLE llm968i_light (asset_id uuid, state text, color text, radius integer, energy double precision,
                                     offset_x integer, offset_y integer, flicker_amplitude double precision,
                                     flicker_period_ms integer) ON COMMIT DROP;
    INSERT INTO llm968i_light
    SELECT * FROM (VALUES
        ('3b42aadd-dc7e-411c-90cc-f3d8395a7c32'::uuid, 'default', '#FF8833', 128, 1::double precision, 0, 0,
         0.15::double precision, 180),
        ('019e5f00-c401-7a10-9e00-000000798002'::uuid, 'burning', '#FF8833', 128, 1::double precision, 0, 0,
         0.15::double precision, 180),
        ('019e5f00-c401-7a10-9e00-000000798002'::uuid, 'dying', '#D9552A', 56, 0.7::double precision, 0, 0,
         0.08::double precision, 400)) v
     WHERE EXISTS (SELECT 1 FROM llm968i_asset x WHERE x.asset_id = v.column1);

    UPDATE asset a SET render_scale = 1 FROM llm968i_asset v WHERE a.id = v.asset_id;
    -- The three tents: the painted tent's footprint, the door on the anchor.
    UPDATE asset
       SET footprint_left = 1, footprint_right = 1, footprint_top = 2, footprint_bottom = 0,
           door_offset_x = 0, door_offset_y = 0
     WHERE id IN ('019e5f00-c401-7a10-9e00-000000798001', '18118703-3aa8-4f28-8ffd-3d3c9f0804ed',
                  'a6f9d413-385f-43e6-9d19-b91f49bc343c');
    -- Existing states only: the check below fails on a missing one.
    UPDATE asset_state s
       SET sheet = v.sheet, src_x = v.src_x, src_y = v.src_y, src_w = v.src_w, src_h = v.src_h,
           frame_count = v.frame_count, frame_rate = v.frame_rate
      FROM llm968i_state v
     WHERE s.asset_id = v.asset_id AND s.state = v.state;
    -- The Camp Fire's lights, written whole; the Campfire's is only checked.
    INSERT INTO asset_state_light (state_id, color, radius, energy, offset_x, offset_y, flicker_amplitude, flicker_period_ms)
    SELECT s.id, v.color, v.radius, v.energy, v.offset_x, v.offset_y, v.flicker_amplitude, v.flicker_period_ms
      FROM llm968i_light v JOIN asset_state s ON s.asset_id = v.asset_id AND s.state = v.state
     WHERE v.asset_id = '019e5f00-c401-7a10-9e00-000000798002'
    ON CONFLICT (state_id) DO UPDATE SET
        color = EXCLUDED.color, radius = EXCLUDED.radius, energy = EXCLUDED.energy, offset_x = EXCLUDED.offset_x,
        offset_y = EXCLUDED.offset_y, flicker_amplitude = EXCLUDED.flicker_amplitude,
        flicker_period_ms = EXCLUDED.flicker_period_ms;

    SELECT count(*) INTO bad FROM (
        (SELECT asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate FROM llm968i_state
         EXCEPT
         SELECT s.asset_id, s.state, s.sheet, s.src_x, s.src_y, s.src_w, s.src_h, s.frame_count, s.frame_rate
           FROM asset_state s JOIN llm968i_asset v ON v.asset_id = s.asset_id)
        UNION ALL
        (SELECT s.asset_id, s.state, s.sheet, s.src_x, s.src_y, s.src_w, s.src_h, s.frame_count, s.frame_rate
           FROM asset_state s JOIN llm968i_asset v ON v.asset_id = s.asset_id
         EXCEPT
         SELECT asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate FROM llm968i_state)
    ) diff;
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968i: % camp state rows differ from the authored values', bad;
    END IF;
    SELECT count(*) INTO bad FROM (
        (SELECT asset_id, state, tag FROM llm968i_tag
         EXCEPT
         SELECT s.asset_id, s.state::text, t.tag::text
           FROM asset_state s JOIN llm968i_asset v ON v.asset_id = s.asset_id JOIN asset_state_tag t ON t.state_id = s.id)
        UNION ALL
        (SELECT s.asset_id, s.state::text, t.tag::text
           FROM asset_state s JOIN llm968i_asset v ON v.asset_id = s.asset_id JOIN asset_state_tag t ON t.state_id = s.id
         EXCEPT
         SELECT asset_id, state, tag FROM llm968i_tag)
    ) diff;
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968i: % camp state tags differ from the expected ones', bad;
    END IF;
    SELECT count(*) INTO bad FROM (
        (SELECT * FROM llm968i_light
         EXCEPT
         SELECT s.asset_id, s.state::text, l.color::text, l.radius, l.energy, l.offset_x, l.offset_y,
                l.flicker_amplitude, l.flicker_period_ms
           FROM asset_state s JOIN llm968i_asset v ON v.asset_id = s.asset_id JOIN asset_state_light l ON l.state_id = s.id)
        UNION ALL
        (SELECT s.asset_id, s.state::text, l.color::text, l.radius, l.energy, l.offset_x, l.offset_y,
                l.flicker_amplitude, l.flicker_period_ms
           FROM asset_state s JOIN llm968i_asset v ON v.asset_id = s.asset_id JOIN asset_state_light l ON l.state_id = s.id
         EXCEPT
         SELECT * FROM llm968i_light)
    ) diff;
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968i: % camp lights differ from the expected values', bad;
    END IF;
    SELECT count(*) INTO bad FROM asset a JOIN llm968i_asset v ON v.asset_id = a.id
     WHERE a.render_scale IS DISTINCT FROM 1::double precision
        OR (a.id IN ('019e5f00-c401-7a10-9e00-000000798001', '18118703-3aa8-4f28-8ffd-3d3c9f0804ed',
                     'a6f9d413-385f-43e6-9d19-b91f49bc343c')
            AND (a.footprint_left, a.footprint_right, a.footprint_top, a.footprint_bottom, a.door_offset_x, a.door_offset_y)
                IS DISTINCT FROM (1, 1, 2, 0, 0, 0));
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968i: % camp asset(s) differ from the authored columns', bad;
    END IF;
END $$;

COMMIT;
