-- LLM-968: painted fences and gates: the Ranch Fence, the Ranch Fence Gate
-- and the High Stone Gate.
--
-- They drew Mana Seed fences-walls art, whose licence forbids use alongside
-- AI. tools/tree-art/fences.py now paints them: client/assets/trees/
-- ranch-fence.png, ranch-fence-gate.png and stone-gate.png, at twice the pack
-- art's size and shown at render_scale 1 (the same size in the world, twice
-- the detail). Anchors do not change, so every placement keeps its place.
--
-- RANCH FENCE (fixed id, created by LLM-637, breaks by LLM-690, so a fresh
-- database has it too): one row of 32x32 cells, one per state, in the order
-- fences.py's piece_names() gives (each state's src_x is its column * 32).
-- The engine picks a segment's state by tag (fence.go fencePieceFor through
-- StateForTag), never by name. Three states are added, end-left, end-right
-- and post, and the tags fence-end-left, fence-end-right and fence-post move
-- to them from corner-bl, corner-br and v-bottom: in the pack's tagging a
-- bottom corner doubled as a line's end and v-bottom as the lone post, and in
-- the painted art those cells draw the rails coming from the north, which on
-- a line's end or a lone post stand up as a stub reaching for nothing (Jeff
-- chose separate pieces, 2026-11-25). Placed segments keep their states; the
-- engine and the editor resolve the new pieces by tag from then on. Every
-- other tag, the break tags included, stays as it is.
--
-- RANCH FENCE GATE: its one state points at its sheet (128x96).
--
-- HIGH STONE GATE: closed and open point at stone-gate.png's two frames
-- (128x128). The pack sheet had its labels swapped (its "closed" frame showed
-- the open arch), and the one placed gate, by the mansion's moat bridge, asked
-- for a "default" state that does not exist, so the client fell back to the
-- first state and drew the open arch. It is meant open (Jeff, 2026-11-25): the
-- asset's default_state becomes open, a placement on a state the asset does
-- not have takes open, and the gate is no longer an obstacle, so the walkway
-- through the arch is passable.
--
-- Each asset that exists is set, then checked both ways against these values
-- (every state's rect, the fence's tags, render_scale, the gate's columns and
-- placements); any difference fails the migration. A missing state of the
-- pack's is not recreated (it would come back without its tags); the check
-- fails on it.
--
-- ENGINE-OWNED data (the catalog, the placements), read at boot; deploy.sh
-- runs stop -> migrate -> start. Convergent: a rerun ends in the same place
-- and repairs a hand edit of a rect or a moved tag.

BEGIN;

DO $$
DECLARE
    fence CONSTANT uuid := '019e5f00-c401-7a10-9e00-000000637001';
    gate CONSTANT uuid := 'e2c34e96-14ef-487c-a6af-d2526d4c4526';
    stone CONSTANT uuid := '6841e98c-4f59-4fff-b3dc-cfff6a222a44';
    bad integer;
BEGIN
    -- The fence's states, as authored: (state, sheet column).
    CREATE TEMP TABLE llm968d_cell (state text PRIMARY KEY, col integer) ON COMMIT DROP;
    INSERT INTO llm968d_cell VALUES
        ('corner-tl', 0), ('h', 1), ('corner-tr', 2), ('v-top', 3), ('v', 4), ('v-bottom', 5),
        ('corner-bl', 6), ('corner-br', 7), ('end-left', 8), ('end-right', 9), ('post', 10),
        ('h-broken-1-l', 11), ('h-broken-1', 12), ('h-broken-1-r', 13),
        ('h-broken-2-l', 14), ('h-broken-2', 15), ('h-broken-2-r', 16),
        ('h-broken-3-l', 17), ('h-broken-3', 18), ('h-broken-3-r', 19),
        ('h-broken-4-l', 20), ('h-broken-4', 21), ('h-broken-4-r', 22);
    -- Its tags, as authored: every piece tag on exactly one state, the break
    -- tags as LLM-690 set them.
    CREATE TEMP TABLE llm968d_tag (state text, tag text) ON COMMIT DROP;
    INSERT INTO llm968d_tag VALUES
        ('corner-tl', 'fence-corner-tl'), ('h', 'fence-h'), ('corner-tr', 'fence-corner-tr'),
        ('v-top', 'fence-v-top'), ('v', 'fence-v'), ('v-bottom', 'fence-v-bottom'),
        ('corner-bl', 'fence-corner-bl'), ('corner-br', 'fence-corner-br'),
        ('end-left', 'fence-end-left'), ('end-right', 'fence-end-right'), ('post', 'fence-post');
    INSERT INTO llm968d_tag
    SELECT v.state, v.tag FROM generate_series(1, 4) k CROSS JOIN LATERAL (VALUES
        ('h-broken-' || k, 'minor-form-fence'), ('h-broken-' || k, 'minor-of-h'),
        ('h-broken-' || k, 'minor-left-h-broken-' || k || '-l'), ('h-broken-' || k, 'minor-right-h-broken-' || k || '-r'),
        ('h-broken-' || k || '-l', 'minor-of-h'), ('h-broken-' || k || '-r', 'minor-of-h')) v(state, tag);

    IF EXISTS (SELECT 1 FROM asset WHERE id = fence) THEN
        UPDATE asset SET render_scale = 1 WHERE id = fence;
        -- The three new pieces (their rects are set with the rest below).
        INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
        SELECT fence, s, 'res://assets/trees/ranch-fence.png', 0, 0, 32, 32, 1, 0
          FROM unnest(ARRAY['end-left', 'end-right', 'post']) s
        ON CONFLICT (asset_id, state) DO NOTHING;
        UPDATE asset_state s
           SET sheet = 'res://assets/trees/ranch-fence.png', src_x = c.col * 32, src_y = 0, src_w = 32, src_h = 32,
               frame_count = 1, frame_rate = 0
          FROM llm968d_cell c
         WHERE s.asset_id = fence AND s.state = c.state;
        -- The moved tags leave the corners and v-bottom and land on their own
        -- pieces.
        DELETE FROM asset_state_tag t USING asset_state s
         WHERE t.state_id = s.id AND s.asset_id = fence
           AND (s.state, t.tag) IN (('corner-bl', 'fence-end-left'), ('corner-br', 'fence-end-right'),
                                    ('v-bottom', 'fence-post'));
        INSERT INTO asset_state_tag (state_id, tag)
        SELECT s.id, v.tag
          FROM asset_state s
          JOIN (VALUES ('end-left', 'fence-end-left'), ('end-right', 'fence-end-right'), ('post', 'fence-post')) v(state, tag)
            ON v.state = s.state
         WHERE s.asset_id = fence
        ON CONFLICT DO NOTHING;

        SELECT count(*) INTO bad FROM (
            (SELECT c.state, 'res://assets/trees/ranch-fence.png' AS sheet, c.col * 32 AS src_x, 0 AS src_y,
                    32 AS src_w, 32 AS src_h, 1 AS frame_count, 0::double precision AS frame_rate FROM llm968d_cell c
             EXCEPT
             SELECT s.state::text, s.sheet::text, s.src_x, s.src_y, s.src_w, s.src_h, s.frame_count, s.frame_rate
               FROM asset_state s WHERE s.asset_id = fence)
            UNION ALL
            (SELECT s.state::text, s.sheet::text, s.src_x, s.src_y, s.src_w, s.src_h, s.frame_count, s.frame_rate
               FROM asset_state s WHERE s.asset_id = fence
             EXCEPT
             SELECT c.state, 'res://assets/trees/ranch-fence.png', c.col * 32, 0, 32, 32, 1, 0 FROM llm968d_cell c)
        ) diff;
        IF bad > 0 THEN
            RAISE EXCEPTION 'LLM-968d: % ranch fence state rows differ from the authored values', bad;
        END IF;
        SELECT count(*) INTO bad FROM (
            (SELECT state, tag FROM llm968d_tag
             EXCEPT
             SELECT s.state::text, t.tag::text FROM asset_state s JOIN asset_state_tag t ON t.state_id = s.id
              WHERE s.asset_id = fence)
            UNION ALL
            (SELECT s.state::text, t.tag::text FROM asset_state s JOIN asset_state_tag t ON t.state_id = s.id
              WHERE s.asset_id = fence
             EXCEPT
             SELECT state, tag FROM llm968d_tag)
        ) diff;
        IF bad > 0 THEN
            RAISE EXCEPTION 'LLM-968d: % ranch fence tags differ from the authored ones', bad;
        END IF;
        IF EXISTS (SELECT 1 FROM asset WHERE id = fence AND render_scale <> 1) THEN
            RAISE EXCEPTION 'LLM-968d: the ranch fence is not at render_scale 1';
        END IF;
    END IF;

    IF EXISTS (SELECT 1 FROM asset WHERE id = gate) THEN
        UPDATE asset SET render_scale = 1 WHERE id = gate;
        UPDATE asset_state
           SET sheet = 'res://assets/trees/ranch-fence-gate.png', src_x = 0, src_y = 0, src_w = 128, src_h = 96,
               frame_count = 1, frame_rate = 0
         WHERE asset_id = gate AND state = 'default';
        IF (SELECT count(*) FROM asset_state WHERE asset_id = gate) <> 1
           OR NOT EXISTS (SELECT 1 FROM asset_state
                           WHERE asset_id = gate AND state = 'default' AND sheet = 'res://assets/trees/ranch-fence-gate.png'
                             AND src_x = 0 AND src_y = 0 AND src_w = 128 AND src_h = 96 AND frame_count = 1 AND frame_rate = 0)
           OR EXISTS (SELECT 1 FROM asset WHERE id = gate AND render_scale <> 1) THEN
            RAISE EXCEPTION 'LLM-968d: the ranch fence gate differs from the authored values';
        END IF;
    END IF;

    IF EXISTS (SELECT 1 FROM asset WHERE id = stone) THEN
        UPDATE asset SET render_scale = 1, default_state = 'open', is_obstacle = false WHERE id = stone;
        UPDATE asset_state s
           SET sheet = 'res://assets/trees/stone-gate.png', src_x = v.x, src_y = 0, src_w = 128, src_h = 128,
               frame_count = 1, frame_rate = 0
          FROM (VALUES ('closed', 0), ('open', 128)) v(state, x)
         WHERE s.asset_id = stone AND s.state = v.state;
        UPDATE village_object SET current_state = 'open'
         WHERE asset_id = stone AND current_state NOT IN ('closed', 'open');
        SELECT count(*) INTO bad FROM (
            (SELECT v.state, v.x FROM (VALUES ('closed', 0), ('open', 128)) v(state, x)
             EXCEPT
             SELECT state::text, src_x FROM asset_state
              WHERE asset_id = stone AND sheet = 'res://assets/trees/stone-gate.png' AND src_y = 0 AND src_w = 128
                AND src_h = 128 AND frame_count = 1 AND frame_rate = 0)
            UNION ALL
            (SELECT state::text, src_x FROM asset_state WHERE asset_id = stone
             EXCEPT
             SELECT v.state, v.x FROM (VALUES ('closed', 0), ('open', 128)) v(state, x))
        ) diff;
        IF bad > 0
           OR NOT EXISTS (SELECT 1 FROM asset WHERE id = stone AND render_scale = 1 AND default_state = 'open'
                                                AND is_obstacle = false)
           OR EXISTS (SELECT 1 FROM village_object WHERE asset_id = stone AND current_state NOT IN ('closed', 'open')) THEN
            RAISE EXCEPTION 'LLM-968d: the high stone gate differs from the authored values';
        END IF;
    END IF;
END $$;

COMMIT;
