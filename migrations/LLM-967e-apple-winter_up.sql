-- LLM-967: the painted apple trees get winter, and apples pause in winter.
--
-- Until now the Apple Tree had only summer and autumn states, so from December
-- to March the season chain (engine/sim/season_art.go) fell through to its
-- autumn look: the orchard stood in leaf while every other tree was bare.
-- tools/tree-art now paints the apple trees' four winter looks, the big trees'
-- set, as rows 5 to 8 of apple-1.png, -2, -3 (one still 48x64 frame each):
-- winter-clean (January to March), winter-leaves (December, dry leaves at the
-- foot), winter-snowy-top (a dusting), winter-snowy. This migration sorts after
-- LLM-967d-apple-tree (salem migrations run in filename order).
--
-- Jeff 2026-11-13: no apples in winter. The engine holds a gatherable empty
-- through winter when its item's winter_regrow_factors entry is 0
-- (engine/sim/winter_regrow.go), and apples go from 125 to 0. So in winter an
-- apple tree has no stock and picks its bare state, which is why the winter
-- states are tagged bare and there is no winter state with apples.
--
-- WHAT this does, when the Apple Tree exists (a fresh database has no
-- catalog; then the catalog part does nothing):
--   1. Apple Tree, Apple Tree 2 and Apple Tree 3 each get the four winter
--      states on their own sheet, rows src_y 256 / 320 / 384 / 448, 48x64,
--      one frame, tagged exactly bare + season-<look>.
--   2. The stored winter_regrow_factors setting, if there is one, has apples=0
--      (replacing the apples entry, or added when it has none). Without a
--      stored value the engine's default already says apples=0.
-- Then both must hold exactly, or the migration fails.
--
-- ENGINE-OWNED data (catalog and setting), read at boot; deploy.sh runs
-- stop -> migrate -> start. Convergent: rerunning ends in the same place, and a
-- rerun repairs a hand edit of these rows.

BEGIN;

DO $$
DECLARE
    apple CONSTANT uuid := '019e5f00-c401-7a10-9e00-000000000623';
    apple2 CONSTANT uuid := '48f8b1e5-a9e6-45d7-87f9-acbbf83fcc61';
    apple3 CONSTANT uuid := 'e8d24766-7ef7-4380-8ff4-0e4de2d9cb05';
    -- one apples entry in an item=percent list, anywhere in it; the engine
    -- reads the number with strconv.Atoi, which takes a sign (apples=+125)
    apples_entry CONSTANT text := '(^|,)\s*apples\s*=\s*[+-]?[0-9]+\s*(,|$)';
    stored text;
    bad integer;
BEGIN
    -- 2. The setting (not part of the catalog, so not gated on it).
    SELECT value INTO stored FROM setting WHERE key = 'winter_regrow_factors';
    IF FOUND THEN
        IF coalesce(stored, '') ~* apples_entry THEN
            stored := regexp_replace(stored, apples_entry, '\1apples=0\2', 'i');
        ELSIF coalesce(btrim(stored), '') = '' THEN
            stored := 'apples=0';
        ELSE
            stored := stored || ',apples=0';
        END IF;
        UPDATE setting SET value = stored WHERE key = 'winter_regrow_factors';
        -- Exactly one apples entry, and it is 0: a second one would make the
        -- engine reject the whole setting and fall back to its default.
        IF stored !~* '(^|,)\s*apples\s*=\s*0\s*(,|$)'
           OR (SELECT count(*) FROM regexp_matches(stored, '(^|,)\s*apples\s*=', 'gi')) <> 1 THEN
            RAISE EXCEPTION 'LLM-967e: winter_regrow_factors does not say apples=0 exactly once: %', stored;
        END IF;
    END IF;

    IF NOT EXISTS (SELECT 1 FROM asset WHERE id = apple) THEN
        RETURN;
    END IF;
    IF (SELECT count(*) FROM asset WHERE id IN (apple2, apple3)) <> 2 THEN
        RAISE EXCEPTION 'LLM-967e: Apple Tree 2 / 3 are missing; LLM-967d-apple-tree must run first';
    END IF;

    CREATE TEMP TABLE llm967e_asset (asset_id uuid PRIMARY KEY, sheet text NOT NULL) ON COMMIT DROP;
    INSERT INTO llm967e_asset VALUES
        (apple,  'res://assets/trees/apple-1.png'),
        (apple2, 'res://assets/trees/apple-2.png'),
        (apple3, 'res://assets/trees/apple-3.png');

    -- Each winter look's row on the sheet.
    CREATE TEMP TABLE llm967e_look (state text PRIMARY KEY, src_y integer NOT NULL) ON COMMIT DROP;
    INSERT INTO llm967e_look VALUES
        ('winter-clean',     256),
        ('winter-leaves',    320),
        ('winter-snowy-top', 384),
        ('winter-snowy',     448);

    -- 1. The states, one still frame each.
    INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
    SELECT v.asset_id, l.state, v.sheet, 0, l.src_y, 48, 64, 1, 0
      FROM llm967e_asset v CROSS JOIN llm967e_look l
    ON CONFLICT (asset_id, state) DO UPDATE
       SET sheet = EXCLUDED.sheet, src_x = EXCLUDED.src_x, src_y = EXCLUDED.src_y, src_w = EXCLUDED.src_w,
           src_h = EXCLUDED.src_h, frame_count = EXCLUDED.frame_count, frame_rate = EXCLUDED.frame_rate;

    -- Their tags, exactly: the picked stock tag and the season look.
    DELETE FROM asset_state_tag
     WHERE state_id IN (SELECT s.id FROM asset_state s JOIN llm967e_look l ON l.state = s.state
                         WHERE s.asset_id IN (apple, apple2, apple3));
    INSERT INTO asset_state_tag (state_id, tag)
    SELECT s.id, t.tag
      FROM asset_state s
      JOIN llm967e_look l ON l.state = s.state
      CROSS JOIN LATERAL (VALUES ('bare'), ('season-' || l.state)) t(tag)
     WHERE s.asset_id IN (apple, apple2, apple3);

    -- Every winter state is as authored.
    SELECT count(*) INTO bad FROM (
        (SELECT v.asset_id, l.state, v.sheet, 0 AS src_x, l.src_y, 48 AS src_w, 64 AS src_h, 1 AS frame_count, 0 AS frame_rate
           FROM llm967e_asset v CROSS JOIN llm967e_look l
         EXCEPT
         SELECT s.asset_id, s.state, s.sheet, s.src_x, s.src_y, s.src_w, s.src_h, s.frame_count, s.frame_rate
           FROM asset_state s JOIN llm967e_look l ON l.state = s.state
          WHERE s.asset_id IN (apple, apple2, apple3))
    ) diff;
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-967e: % apple tree winter states differ from the authored values', bad;
    END IF;

    -- And tagged exactly bare + its season look: 12 states, 24 tags.
    SELECT count(*) INTO bad FROM (
        SELECT s.id
          FROM asset_state s
          JOIN llm967e_look l ON l.state = s.state
          LEFT JOIN asset_state_tag t ON t.state_id = s.id
         WHERE s.asset_id IN (apple, apple2, apple3)
         GROUP BY s.id, l.state
        HAVING count(t.tag) <> 2
            OR count(*) FILTER (WHERE t.tag = 'bare') <> 1
            OR count(*) FILTER (WHERE t.tag = 'season-' || l.state) <> 1
    ) wrong;
    IF bad > 0 OR (SELECT count(*) FROM asset_state s JOIN llm967e_look l ON l.state = s.state
                    WHERE s.asset_id IN (apple, apple2, apple3)) <> 12 THEN
        RAISE EXCEPTION 'LLM-967e: % apple tree winter states are not tagged bare + their season look', bad;
    END IF;
END $$;

COMMIT;
