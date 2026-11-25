-- LLM-968: painted lamps: Lamp Post, Lamp Post 2, Lamp Post (Sign) and the
-- Torch.
--
-- They drew Mana Seed village-accessories art, whose licence forbids use
-- alongside AI. tools/tree-art/lamps.py now paints them: client/assets/trees/
-- lamp-post.png, lamp-post-2.png, lamp-post-sign.png and torch.png, at twice
-- the pack art's size and shown at render_scale 1 (the same size in the world,
-- twice the detail). Anchors do not change, so every placement keeps its place
-- and every light (drawn at the anchor) its pool.
--
-- Sheets, one row each: the lamp posts unlit then lit (96x160 frames), the
-- sign post one frame (96x160), the torch unlit then its four lit flame frames
-- (32x96; the lit state keeps 4 frames at 8 a second). No state, tag or
-- placement changes: the lamplighter and the day/night cycle pick states by
-- their tags (day-active, night-active, lamplighter-target), checked here.
--
-- LIGHTS: the two lamp posts' lit lights now flicker a little, as a candle in
-- a lantern does (amplitude 0.05, period 400 ms; Jeff, 2026-11-25). Before,
-- they were steady (0 / 0). The torch's light already flickers (0.15, 180 ms)
-- and is unchanged. Every other light value stays as it is, and is checked.
--
-- Each asset that exists is set, then its states, tags and lights are
-- checked both ways against these values; any difference fails the
-- migration. A missing state is not recreated (it would come back without its
-- tags); the check fails on it. An asset not in this database takes no part
-- (none of the four is created by a migration).
--
-- ENGINE-OWNED data (the catalog), read at boot; deploy.sh runs stop ->
-- migrate -> start. Convergent: a rerun ends in the same place and repairs a
-- hand edit of a rect or a lamp post's flicker (the up writes the authored
-- values over whatever is there; it does not keep them to restore).

BEGIN;

-- Hold off any other catalog writer (the editor) between the presence check
-- and the checks below.
LOCK TABLE asset, asset_state, asset_state_tag, asset_state_light IN SHARE ROW EXCLUSIVE MODE;

DO $$
DECLARE
    bad integer;
BEGIN
    CREATE TEMP TABLE llm968e_lamp (asset_id uuid PRIMARY KEY) ON COMMIT DROP;
    INSERT INTO llm968e_lamp VALUES
        ('734bb07d-001f-4cd7-8514-9d72113cd8b7'),   -- Lamp Post
        ('3741d509-61b5-4823-926a-da4add56fc15'),   -- Lamp Post 2
        ('dbc93a7b-ff3d-42e6-b4e1-d6e36ca7a4fd'),   -- Lamp Post (Sign)
        ('4ffca19a-2b3e-4ed3-844f-41d25f15a3c6');   -- Torch
    DELETE FROM llm968e_lamp v WHERE NOT EXISTS (SELECT 1 FROM asset WHERE id = v.asset_id);
    IF NOT EXISTS (SELECT 1 FROM llm968e_lamp) THEN
        RETURN;
    END IF;

    -- Every state, as authored.
    CREATE TEMP TABLE llm968e_state (asset_id uuid, state text, sheet text, src_x integer, src_y integer,
                                     src_w integer, src_h integer, frame_count integer,
                                     frame_rate double precision) ON COMMIT DROP;
    INSERT INTO llm968e_state VALUES
        ('734bb07d-001f-4cd7-8514-9d72113cd8b7', 'unlit', 'res://assets/trees/lamp-post.png', 0, 0, 96, 160, 1, 0),
        ('734bb07d-001f-4cd7-8514-9d72113cd8b7', 'lit', 'res://assets/trees/lamp-post.png', 96, 0, 96, 160, 1, 0),
        ('3741d509-61b5-4823-926a-da4add56fc15', 'unlit', 'res://assets/trees/lamp-post-2.png', 0, 0, 96, 160, 1, 0),
        ('3741d509-61b5-4823-926a-da4add56fc15', 'lit', 'res://assets/trees/lamp-post-2.png', 96, 0, 96, 160, 1, 0),
        ('dbc93a7b-ff3d-42e6-b4e1-d6e36ca7a4fd', 'default', 'res://assets/trees/lamp-post-sign.png', 0, 0, 96, 160, 1, 0),
        ('4ffca19a-2b3e-4ed3-844f-41d25f15a3c6', 'unlit', 'res://assets/trees/torch.png', 0, 0, 32, 96, 1, 0),
        ('4ffca19a-2b3e-4ed3-844f-41d25f15a3c6', 'default', 'res://assets/trees/torch.png', 32, 0, 32, 96, 4, 8);
    DELETE FROM llm968e_state v WHERE NOT EXISTS (SELECT 1 FROM llm968e_lamp l WHERE l.asset_id = v.asset_id);

    -- Their tags, unchanged by this migration: the lamplighter and the
    -- day/night flip find a lamp's states by them.
    CREATE TEMP TABLE llm968e_tag (asset_id uuid, state text, tag text) ON COMMIT DROP;
    INSERT INTO llm968e_tag
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
     WHERE EXISTS (SELECT 1 FROM llm968e_lamp l WHERE l.asset_id = v.a);

    -- Their lights: the two lamp posts' with the new flicker, the torch's as
    -- it is.
    CREATE TEMP TABLE llm968e_light (asset_id uuid, state text, color text, radius integer,
                                     energy double precision, offset_x integer, offset_y integer,
                                     flicker_amplitude double precision, flicker_period_ms integer) ON COMMIT DROP;
    INSERT INTO llm968e_light
    SELECT * FROM (VALUES
        ('734bb07d-001f-4cd7-8514-9d72113cd8b7'::uuid, 'lit', '#FFAA55', 96, 1.0::double precision, 0, 0,
         0.05::double precision, 400),
        ('3741d509-61b5-4823-926a-da4add56fc15'::uuid, 'lit', '#FFAA55', 96, 1.0::double precision, 0, 0,
         0.05::double precision, 400),
        ('4ffca19a-2b3e-4ed3-844f-41d25f15a3c6'::uuid, 'default', '#FF9944', 72, 1.0::double precision, 0, 0,
         0.15::double precision, 180)) v
     WHERE EXISTS (SELECT 1 FROM llm968e_lamp l WHERE l.asset_id = v.column1);

    UPDATE asset a SET render_scale = 1 FROM llm968e_lamp v WHERE a.id = v.asset_id;
    -- Existing states only: the check below fails on a missing one.
    UPDATE asset_state s
       SET sheet = v.sheet, src_x = v.src_x, src_y = v.src_y, src_w = v.src_w, src_h = v.src_h,
           frame_count = v.frame_count, frame_rate = v.frame_rate
      FROM llm968e_state v
     WHERE s.asset_id = v.asset_id AND s.state = v.state;
    -- Only the flicker is set; a lamp's missing light or a changed colour,
    -- radius or energy fails the check below.
    UPDATE asset_state_light l
       SET flicker_amplitude = v.flicker_amplitude, flicker_period_ms = v.flicker_period_ms
      FROM asset_state s, llm968e_light v
     WHERE l.state_id = s.id AND s.asset_id = v.asset_id AND s.state = v.state
       -- The lamp posts' lights only: the torch's (state default) is never
       -- written, only checked.
       AND v.state = 'lit';

    SELECT count(*) INTO bad FROM (
        (SELECT asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate FROM llm968e_state
         EXCEPT
         SELECT s.asset_id, s.state, s.sheet, s.src_x, s.src_y, s.src_w, s.src_h, s.frame_count, s.frame_rate
           FROM asset_state s JOIN llm968e_lamp v ON v.asset_id = s.asset_id)
        UNION ALL
        (SELECT s.asset_id, s.state, s.sheet, s.src_x, s.src_y, s.src_w, s.src_h, s.frame_count, s.frame_rate
           FROM asset_state s JOIN llm968e_lamp v ON v.asset_id = s.asset_id
         EXCEPT
         SELECT asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate FROM llm968e_state)
    ) diff;
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968e: % lamp state rows differ from the authored values', bad;
    END IF;
    SELECT count(*) INTO bad FROM (
        (SELECT asset_id, state, tag FROM llm968e_tag
         EXCEPT
         SELECT s.asset_id, s.state::text, t.tag::text
           FROM asset_state s JOIN llm968e_lamp v ON v.asset_id = s.asset_id JOIN asset_state_tag t ON t.state_id = s.id)
        UNION ALL
        (SELECT s.asset_id, s.state::text, t.tag::text
           FROM asset_state s JOIN llm968e_lamp v ON v.asset_id = s.asset_id JOIN asset_state_tag t ON t.state_id = s.id
         EXCEPT
         SELECT asset_id, state, tag FROM llm968e_tag)
    ) diff;
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968e: % lamp state tags differ from the expected ones', bad;
    END IF;
    SELECT count(*) INTO bad FROM (
        (SELECT * FROM llm968e_light
         EXCEPT
         SELECT s.asset_id, s.state::text, l.color::text, l.radius, l.energy, l.offset_x, l.offset_y,
                l.flicker_amplitude, l.flicker_period_ms
           FROM asset_state s JOIN llm968e_lamp v ON v.asset_id = s.asset_id JOIN asset_state_light l ON l.state_id = s.id)
        UNION ALL
        (SELECT s.asset_id, s.state::text, l.color::text, l.radius, l.energy, l.offset_x, l.offset_y,
                l.flicker_amplitude, l.flicker_period_ms
           FROM asset_state s JOIN llm968e_lamp v ON v.asset_id = s.asset_id JOIN asset_state_light l ON l.state_id = s.id
         EXCEPT
         SELECT * FROM llm968e_light)
    ) diff;
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968e: % lamp lights differ from the expected values', bad;
    END IF;
    SELECT count(*) INTO bad FROM asset a JOIN llm968e_lamp v ON v.asset_id = a.id WHERE a.render_scale <> 1;
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968e: % lamp(s) not at render_scale 1', bad;
    END IF;
END $$;

COMMIT;
