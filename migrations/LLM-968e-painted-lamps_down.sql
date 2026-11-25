-- LLM-968 down: Lamp Post, Lamp Post 2, Lamp Post (Sign) and the Torch back
-- on the Mana Seed village-accessories sheets at render_scale 2, as the pack
-- catalog set them, and the two lamp posts' lit lights steady again (flicker
-- 0 / 0). Tags and the torch's light are unchanged, and checked.
--
-- This is a reset to the pack values, not an undo of whatever the up found:
-- the up keeps no record of the values it overwrote (a hand-tuned flicker on
-- a lamp post, say, comes back as 0 / 0).
--
-- Each asset that exists is set, then checked both ways; any difference fails
-- the migration. Convergent.

BEGIN;

-- Hold off any other catalog writer (the editor) between the presence check
-- and the checks below.
LOCK TABLE asset, asset_state, asset_state_tag, asset_state_light IN SHARE ROW EXCLUSIVE MODE;

DO $$
DECLARE
    bad integer;
BEGIN
    CREATE TEMP TABLE llm968e_down_lamp (asset_id uuid PRIMARY KEY) ON COMMIT DROP;
    INSERT INTO llm968e_down_lamp VALUES
        ('734bb07d-001f-4cd7-8514-9d72113cd8b7'),   -- Lamp Post
        ('3741d509-61b5-4823-926a-da4add56fc15'),   -- Lamp Post 2
        ('dbc93a7b-ff3d-42e6-b4e1-d6e36ca7a4fd'),   -- Lamp Post (Sign)
        ('4ffca19a-2b3e-4ed3-844f-41d25f15a3c6');   -- Torch
    DELETE FROM llm968e_down_lamp v WHERE NOT EXISTS (SELECT 1 FROM asset WHERE id = v.asset_id);
    IF NOT EXISTS (SELECT 1 FROM llm968e_down_lamp) THEN
        RETURN;
    END IF;

    -- Every state, as the pack catalog had it.
    CREATE TEMP TABLE llm968e_down_state (asset_id uuid, state text, sheet text, src_x integer, src_y integer,
                                     src_w integer, src_h integer, frame_count integer,
                                     frame_rate double precision) ON COMMIT DROP;
    INSERT INTO llm968e_down_state VALUES
        ('734bb07d-001f-4cd7-8514-9d72113cd8b7', 'unlit', '/tilesets/mana-seed/village-accessories/village accessories 48x80.png', 192, 80, 48, 80, 1, 0),
        ('734bb07d-001f-4cd7-8514-9d72113cd8b7', 'lit', '/tilesets/mana-seed/village-accessories/village accessories 48x80.png', 240, 80, 48, 80, 1, 0),
        ('3741d509-61b5-4823-926a-da4add56fc15', 'unlit', '/tilesets/mana-seed/village-accessories/village accessories 48x80.png', 0, 80, 48, 80, 1, 0),
        ('3741d509-61b5-4823-926a-da4add56fc15', 'lit', '/tilesets/mana-seed/village-accessories/village accessories 48x80.png', 48, 80, 48, 80, 1, 0),
        ('dbc93a7b-ff3d-42e6-b4e1-d6e36ca7a4fd', 'default', '/tilesets/mana-seed/village-accessories/village accessories 48x80.png', 336, 80, 48, 80, 1, 0),
        ('4ffca19a-2b3e-4ed3-844f-41d25f15a3c6', 'unlit', '/tilesets/mana-seed/village-accessories/village anim 16x48.png', 0, 0, 16, 48, 1, 0),
        ('4ffca19a-2b3e-4ed3-844f-41d25f15a3c6', 'default', '/tilesets/mana-seed/village-accessories/village anim 16x48.png', 16, 0, 16, 48, 4, 8);
    DELETE FROM llm968e_down_state v WHERE NOT EXISTS (SELECT 1 FROM llm968e_down_lamp l WHERE l.asset_id = v.asset_id);

    -- Their tags, unchanged by this migration: the lamplighter and the
    -- day/night flip find a lamp's states by them.
    CREATE TEMP TABLE llm968e_down_tag (asset_id uuid, state text, tag text) ON COMMIT DROP;
    INSERT INTO llm968e_down_tag
    SELECT a, s, t FROM (VALUES
        ('734bb07d-001f-4cd7-8514-9d72113cd8b7'::uuid, 'unlit', 'day-active'),
        ('734bb07d-001f-4cd7-8514-9d72113cd8b7'::uuid, 'unlit', 'lamplighter-target'),
        ('734bb07d-001f-4cd7-8514-9d72113cd8b7'::uuid, 'lit', 'night-active'),
        ('734bb07d-001f-4cd7-8514-9d72113cd8b7'::uuid, 'lit', 'lamplighter-target'),
        ('3741d509-61b5-4823-926a-da4add56fc15'::uuid, 'unlit', 'day-active'),
        ('3741d509-61b5-4823-926a-da4add56fc15'::uuid, 'unlit', 'lamplighter-target'),
        ('3741d509-61b5-4823-926a-da4add56fc15'::uuid, 'lit', 'night-active'),
        ('3741d509-61b5-4823-926a-da4add56fc15'::uuid, 'lit', 'lamplighter-target'),
        ('4ffca19a-2b3e-4ed3-844f-41d25f15a3c6'::uuid, 'unlit', 'day-active'),
        ('4ffca19a-2b3e-4ed3-844f-41d25f15a3c6'::uuid, 'unlit', 'lamplighter-target'),
        ('4ffca19a-2b3e-4ed3-844f-41d25f15a3c6'::uuid, 'default', 'night-active'),
        ('4ffca19a-2b3e-4ed3-844f-41d25f15a3c6'::uuid, 'default', 'lamplighter-target')) v(a, s, t)
     WHERE EXISTS (SELECT 1 FROM llm968e_down_lamp l WHERE l.asset_id = v.a);

    -- Their lights: the two lamp posts' steady again, the torch's as it is.
    CREATE TEMP TABLE llm968e_down_light (asset_id uuid, state text, color text, radius integer,
                                     energy double precision, offset_x integer, offset_y integer,
                                     flicker_amplitude double precision, flicker_period_ms integer) ON COMMIT DROP;
    INSERT INTO llm968e_down_light
    SELECT * FROM (VALUES
        ('734bb07d-001f-4cd7-8514-9d72113cd8b7'::uuid, 'lit', '#FFAA55', 96, 1.0::double precision, 0, 0,
         0::double precision, 0),
        ('3741d509-61b5-4823-926a-da4add56fc15'::uuid, 'lit', '#FFAA55', 96, 1.0::double precision, 0, 0,
         0::double precision, 0),
        ('4ffca19a-2b3e-4ed3-844f-41d25f15a3c6'::uuid, 'default', '#FF9944', 72, 1.0::double precision, 0, 0,
         0.15::double precision, 180)) v
     WHERE EXISTS (SELECT 1 FROM llm968e_down_lamp l WHERE l.asset_id = v.column1);

    UPDATE asset a SET render_scale = 2 FROM llm968e_down_lamp v WHERE a.id = v.asset_id;
    -- Existing states only: the check below fails on a missing one.
    UPDATE asset_state s
       SET sheet = v.sheet, src_x = v.src_x, src_y = v.src_y, src_w = v.src_w, src_h = v.src_h,
           frame_count = v.frame_count, frame_rate = v.frame_rate
      FROM llm968e_down_state v
     WHERE s.asset_id = v.asset_id AND s.state = v.state;
    -- Only the flicker is set; a lamp's missing light or a changed colour,
    -- radius or energy fails the check below.
    UPDATE asset_state_light l
       SET flicker_amplitude = v.flicker_amplitude, flicker_period_ms = v.flicker_period_ms
      FROM asset_state s, llm968e_down_light v
     WHERE l.state_id = s.id AND s.asset_id = v.asset_id AND s.state = v.state
       -- The lamp posts' lights only: the torch's (state default) is never
       -- written, only checked.
       AND v.state = 'lit';

    SELECT count(*) INTO bad FROM (
        (SELECT asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate FROM llm968e_down_state
         EXCEPT
         SELECT s.asset_id, s.state, s.sheet, s.src_x, s.src_y, s.src_w, s.src_h, s.frame_count, s.frame_rate
           FROM asset_state s JOIN llm968e_down_lamp v ON v.asset_id = s.asset_id)
        UNION ALL
        (SELECT s.asset_id, s.state, s.sheet, s.src_x, s.src_y, s.src_w, s.src_h, s.frame_count, s.frame_rate
           FROM asset_state s JOIN llm968e_down_lamp v ON v.asset_id = s.asset_id
         EXCEPT
         SELECT asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate FROM llm968e_down_state)
    ) diff;
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968e down: % lamp state rows differ from the pack values', bad;
    END IF;
    SELECT count(*) INTO bad FROM (
        (SELECT asset_id, state, tag FROM llm968e_down_tag
         EXCEPT
         SELECT s.asset_id, s.state::text, t.tag::text
           FROM asset_state s JOIN llm968e_down_lamp v ON v.asset_id = s.asset_id JOIN asset_state_tag t ON t.state_id = s.id)
        UNION ALL
        (SELECT s.asset_id, s.state::text, t.tag::text
           FROM asset_state s JOIN llm968e_down_lamp v ON v.asset_id = s.asset_id JOIN asset_state_tag t ON t.state_id = s.id
         EXCEPT
         SELECT asset_id, state, tag FROM llm968e_down_tag)
    ) diff;
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968e down: % lamp state tags differ from the expected ones', bad;
    END IF;
    SELECT count(*) INTO bad FROM (
        (SELECT * FROM llm968e_down_light
         EXCEPT
         SELECT s.asset_id, s.state::text, l.color::text, l.radius, l.energy, l.offset_x, l.offset_y,
                l.flicker_amplitude, l.flicker_period_ms
           FROM asset_state s JOIN llm968e_down_lamp v ON v.asset_id = s.asset_id JOIN asset_state_light l ON l.state_id = s.id)
        UNION ALL
        (SELECT s.asset_id, s.state::text, l.color::text, l.radius, l.energy, l.offset_x, l.offset_y,
                l.flicker_amplitude, l.flicker_period_ms
           FROM asset_state s JOIN llm968e_down_lamp v ON v.asset_id = s.asset_id JOIN asset_state_light l ON l.state_id = s.id
         EXCEPT
         SELECT * FROM llm968e_down_light)
    ) diff;
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968e down: % lamp lights differ from the expected values', bad;
    END IF;
    SELECT count(*) INTO bad FROM asset a JOIN llm968e_down_lamp v ON v.asset_id = a.id WHERE a.render_scale <> 2;
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968e down: % lamp(s) not back at render_scale 2', bad;
    END IF;
END $$;

COMMIT;
