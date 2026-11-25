-- LLM-968 down: the Ranch Fence, Ranch Fence Gate and High Stone Gate back on
-- the Mana Seed fences-walls sheets at render_scale 2, as LLM-637 and LLM-690
-- (and the pack catalog for the gates) set them.
--
-- RANCH FENCE: a placed segment on end-left, end-right or post moves to the
-- state that carried that piece's tag before (corner-bl, corner-br,
-- v-bottom); the three states are deleted (their tags go with them) and the
-- tags return to those states. Every state's rect returns to the pack cell.
--
-- HIGH STONE GATE: back to its pack rects (the swapped labels with them),
-- default_state "default" and an obstacle again. A placement on "open" goes
-- back to "default", which is what the one placed gate asked for before the
-- up (the client drew the pack's first state for it, the open arch; the pack's
-- own "open" frame shows the gate shut).
--
-- Each asset that exists is set, then checked both ways; any difference fails
-- the migration. Convergent.

BEGIN;

DO $$
DECLARE
    fence CONSTANT uuid := '019e5f00-c401-7a10-9e00-000000637001';
    gate CONSTANT uuid := 'e2c34e96-14ef-487c-a6af-d2526d4c4526';
    stone CONSTANT uuid := '6841e98c-4f59-4fff-b3dc-cfff6a222a44';
    pack CONSTANT text := '/tilesets/mana-seed/fences-walls/ranch style fence 16x16.png';
    bad integer;
BEGIN
    CREATE TEMP TABLE llm968d_down_cell (state text PRIMARY KEY, x integer, y integer) ON COMMIT DROP;
    INSERT INTO llm968d_down_cell VALUES
        ('corner-tl', 0, 0), ('h', 16, 0), ('corner-tr', 32, 0), ('v-top', 48, 0), ('v', 48, 16), ('v-bottom', 48, 32),
        ('corner-bl', 0, 32), ('corner-br', 32, 32),
        ('h-broken-1-l', 64, 0), ('h-broken-1', 80, 0), ('h-broken-1-r', 96, 0),
        ('h-broken-2-l', 64, 16), ('h-broken-2', 80, 16), ('h-broken-2-r', 96, 16),
        ('h-broken-3-l', 64, 32), ('h-broken-3', 80, 32), ('h-broken-3-r', 96, 32),
        ('h-broken-4-l', 64, 48), ('h-broken-4', 80, 48), ('h-broken-4-r', 96, 48);
    CREATE TEMP TABLE llm968d_down_tag (state text, tag text) ON COMMIT DROP;
    INSERT INTO llm968d_down_tag VALUES
        ('corner-tl', 'fence-corner-tl'), ('h', 'fence-h'), ('corner-tr', 'fence-corner-tr'),
        ('v-top', 'fence-v-top'), ('v', 'fence-v'), ('v-bottom', 'fence-v-bottom'), ('v-bottom', 'fence-post'),
        ('corner-bl', 'fence-corner-bl'), ('corner-bl', 'fence-end-left'),
        ('corner-br', 'fence-corner-br'), ('corner-br', 'fence-end-right');
    INSERT INTO llm968d_down_tag
    SELECT v.state, v.tag FROM generate_series(1, 4) k CROSS JOIN LATERAL (VALUES
        ('h-broken-' || k, 'minor-form-fence'), ('h-broken-' || k, 'minor-of-h'),
        ('h-broken-' || k, 'minor-left-h-broken-' || k || '-l'), ('h-broken-' || k, 'minor-right-h-broken-' || k || '-r'),
        ('h-broken-' || k || '-l', 'minor-of-h'), ('h-broken-' || k || '-r', 'minor-of-h')) v(state, tag);

    IF EXISTS (SELECT 1 FROM asset WHERE id = fence) THEN
        -- Deleting the three states cascades their tags, so first refuse if
        -- they carry anything but their own piece tag: it would be lost
        -- before the checks below could see it.
        SELECT count(*) INTO bad
          FROM asset_state s JOIN asset_state_tag t ON t.state_id = s.id
         WHERE s.asset_id = fence AND s.state IN ('end-left', 'end-right', 'post')
           AND (s.state::text, t.tag::text) NOT IN (('end-left', 'fence-end-left'), ('end-right', 'fence-end-right'),
                                                    ('post', 'fence-post'));
        IF bad > 0 THEN
            RAISE EXCEPTION 'LLM-968d down: % unexpected tag(s) on end-left / end-right / post', bad;
        END IF;
        UPDATE village_object vo SET current_state = m.back
          FROM (VALUES ('end-left', 'corner-bl'), ('end-right', 'corner-br'), ('post', 'v-bottom')) m(piece, back)
         WHERE vo.asset_id = fence AND vo.current_state = m.piece;
        DELETE FROM asset_state WHERE asset_id = fence AND state IN ('end-left', 'end-right', 'post');
        INSERT INTO asset_state_tag (state_id, tag)
        SELECT s.id, v.tag
          FROM asset_state s
          JOIN (VALUES ('corner-bl', 'fence-end-left'), ('corner-br', 'fence-end-right'), ('v-bottom', 'fence-post')) v(state, tag)
            ON v.state = s.state
         WHERE s.asset_id = fence
        ON CONFLICT DO NOTHING;
        UPDATE asset_state s
           SET sheet = pack, src_x = c.x, src_y = c.y, src_w = 16, src_h = 16, frame_count = 1, frame_rate = 0
          FROM llm968d_down_cell c
         WHERE s.asset_id = fence AND s.state = c.state;
        UPDATE asset SET render_scale = 2 WHERE id = fence;

        SELECT count(*) INTO bad FROM (
            (SELECT c.state, pack AS sheet, c.x, c.y, 16 AS w, 16 AS h, 1 AS fc, 0::double precision AS fr
               FROM llm968d_down_cell c
             EXCEPT
             SELECT s.state::text, s.sheet::text, s.src_x, s.src_y, s.src_w, s.src_h, s.frame_count, s.frame_rate
               FROM asset_state s WHERE s.asset_id = fence)
            UNION ALL
            (SELECT s.state::text, s.sheet::text, s.src_x, s.src_y, s.src_w, s.src_h, s.frame_count, s.frame_rate
               FROM asset_state s WHERE s.asset_id = fence
             EXCEPT
             SELECT c.state, pack, c.x, c.y, 16, 16, 1, 0 FROM llm968d_down_cell c)
        ) diff;
        IF bad > 0 THEN
            RAISE EXCEPTION 'LLM-968d down: % ranch fence state rows differ from the pack values', bad;
        END IF;
        SELECT count(*) INTO bad FROM (
            (SELECT state, tag FROM llm968d_down_tag
             EXCEPT
             SELECT s.state::text, t.tag::text FROM asset_state s JOIN asset_state_tag t ON t.state_id = s.id
              WHERE s.asset_id = fence)
            UNION ALL
            (SELECT s.state::text, t.tag::text FROM asset_state s JOIN asset_state_tag t ON t.state_id = s.id
              WHERE s.asset_id = fence
             EXCEPT
             SELECT state, tag FROM llm968d_down_tag)
        ) diff;
        IF bad > 0 THEN
            RAISE EXCEPTION 'LLM-968d down: % ranch fence tags differ from the pack tagging', bad;
        END IF;
        IF EXISTS (SELECT 1 FROM asset WHERE id = fence AND render_scale <> 2) THEN
            RAISE EXCEPTION 'LLM-968d down: the ranch fence is not back at render_scale 2';
        END IF;
    END IF;

    IF EXISTS (SELECT 1 FROM asset WHERE id = gate) THEN
        UPDATE asset SET render_scale = 2 WHERE id = gate;
        UPDATE asset_state
           SET sheet = '/tilesets/mana-seed/fences-walls/ranch style fence gate 64x48.png', src_x = 0, src_y = 0,
               src_w = 64, src_h = 48, frame_count = 1, frame_rate = 0
         WHERE asset_id = gate AND state = 'default';
        IF (SELECT count(*) FROM asset_state WHERE asset_id = gate) <> 1
           OR NOT EXISTS (SELECT 1 FROM asset_state
                           WHERE asset_id = gate AND state = 'default'
                             AND sheet = '/tilesets/mana-seed/fences-walls/ranch style fence gate 64x48.png'
                             AND src_x = 0 AND src_y = 0 AND src_w = 64 AND src_h = 48 AND frame_count = 1 AND frame_rate = 0)
           OR EXISTS (SELECT 1 FROM asset WHERE id = gate AND render_scale <> 2) THEN
            RAISE EXCEPTION 'LLM-968d down: the ranch fence gate differs from the pack values';
        END IF;
    END IF;

    IF EXISTS (SELECT 1 FROM asset WHERE id = stone) THEN
        UPDATE asset SET render_scale = 2, default_state = 'default', is_obstacle = true WHERE id = stone;
        UPDATE asset_state s
           SET sheet = '/tilesets/mana-seed/fences-walls/high stone wall gate 64x64.png', src_x = v.x, src_y = 0,
               src_w = 64, src_h = 64, frame_count = 1, frame_rate = 0
          FROM (VALUES ('closed', 64), ('open', 128)) v(state, x)
         WHERE s.asset_id = stone AND s.state = v.state;
        UPDATE village_object SET current_state = 'default' WHERE asset_id = stone AND current_state = 'open';
        SELECT count(*) INTO bad FROM (
            (SELECT v.state, v.x FROM (VALUES ('closed', 64), ('open', 128)) v(state, x)
             EXCEPT
             SELECT state::text, src_x FROM asset_state
              WHERE asset_id = stone AND sheet = '/tilesets/mana-seed/fences-walls/high stone wall gate 64x64.png'
                AND src_y = 0 AND src_w = 64 AND src_h = 64 AND frame_count = 1 AND frame_rate = 0)
            UNION ALL
            (SELECT state::text, src_x FROM asset_state WHERE asset_id = stone
             EXCEPT
             SELECT v.state, v.x FROM (VALUES ('closed', 64), ('open', 128)) v(state, x))
        ) diff;
        IF bad > 0
           OR NOT EXISTS (SELECT 1 FROM asset WHERE id = stone AND render_scale = 2 AND default_state = 'default'
                                                AND is_obstacle = true) THEN
            RAISE EXCEPTION 'LLM-968d down: the high stone gate differs from the pack values';
        END IF;
    END IF;
END $$;

COMMIT;
