-- LLM-849 (seasons epic LLM-834, slice 1c): autumn and winter art for the trees,
-- bushes and forest props.
--
-- The four Mana Seed forest packs share one layout: every season's sheet has the
-- summer sheet's pixel size, and the same src rect cuts the same object. So a
-- season state is the summer state with only `sheet` changed. Each new state is
-- tagged `season-<look>`; the engine (season_art.go) picks an object's state from
-- the look chain for the month and the snow tier, and a state with no season tag
-- is the summer look. Spring is summer art (Jeff, 2026-10-23), so no spring rows.
--
-- WHAT this adds, from every asset_state on a `summer-forest/summer sheets/`
-- sheet (47 states on 46 assets at the time of writing):
--   * trees 80x112      → autumn-clean, autumn-leaves, autumn-bare,
--                          winter-clean, winter-leaves, winter-snowy-top, winter-snowy
--   * 32x32, 48x32      → autumn, winter-clean, winter-leaves, winter-snowy-top, winter-snowy
--   * 16x16, 16x32      → autumn, winter-clean, winter-snowy
-- Blueberry Bush, Raspberry Bush and Sage Bush get NO winter rows: in the winter
-- art `berries` and `bare` are the same dead twigs, so a stocked bush would look
-- empty. With no winter rows the look chain falls back to their autumn art.
--
-- A source state other than `default` keeps its name as a suffix and its tags,
-- so a bush's `berries` / `bare` stock states become `autumn-berries` /
-- `autumn-bare` carrying the same `berries` / `bare` tag (berry_state.go picks
-- the stock half and the season half together).
--
-- By hand:
--   * Apple Tree — its sheet is the growable fruit-tree sheet, not a forest pack.
--     Autumn is row 2 of the same sheet: bare 144,128 and berries 192,128. No
--     winter rows (fruit on the winter rows "looks really weird", Jeff).
--   * Tiny Swords Tree 3 / Tree 4 — home built season sheets (tiles repo
--     `71c7be4`, tools/build-tree-seasons.ps1): `-autumn`, `-winter`,
--     `-winter-snowy`, the default row's 8-frame sway with only `sheet` changed.
--   Tree 1 / Tree 2 (pines) and the crops get no season art.
--
-- The art is on the VPS beside summer-forest (`/tilesets/mana-seed/
-- {autumn,winter}-forest/...` and `/tilesets/tiny-swords/resources/trees/`).
--
-- ENGINE-OWNED reference data, read at boot; deploy.sh runs stop -> migrate ->
-- start. Rerun-safe: state rows NOT EXISTS-guarded on (asset_id, state), tag rows
-- ON CONFLICT DO NOTHING. Loud validation at the end where the catalog exists.

BEGIN;

CREATE TEMP TABLE llm849_look (
    summer_sheet text NOT NULL,
    look         text NOT NULL,
    sheet        text NOT NULL,
    winter       boolean NOT NULL
) ON COMMIT DROP;

INSERT INTO llm849_look (summer_sheet, look, sheet, winter) VALUES
    ('summer trees 80x112.png', 'autumn-clean',     '/tilesets/mana-seed/autumn-forest/autumn sheets/autumn trees (clean) 80x112.png', false),
    ('summer trees 80x112.png', 'autumn-leaves',    '/tilesets/mana-seed/autumn-forest/autumn sheets/autumn trees (leaves) 80x112.png', false),
    ('summer trees 80x112.png', 'autumn-bare',      '/tilesets/mana-seed/autumn-forest/autumn sheets/autumn trees (bare) 80x112.png', false),
    ('summer trees 80x112.png', 'winter-clean',     '/tilesets/mana-seed/winter-forest/winter sheets/winter trees (clean) 80x112.png', true),
    ('summer trees 80x112.png', 'winter-leaves',    '/tilesets/mana-seed/winter-forest/winter sheets/winter trees (leaves) 80x112.png', true),
    ('summer trees 80x112.png', 'winter-snowy-top', '/tilesets/mana-seed/winter-forest/winter sheets/winter trees (snowy top) 80x112.png', true),
    ('summer trees 80x112.png', 'winter-snowy',     '/tilesets/mana-seed/winter-forest/winter sheets/winter trees (snowy) 80x112.png', true),

    ('summer 32x32.png', 'autumn',           '/tilesets/mana-seed/autumn-forest/autumn sheets/autumn 32x32.png', false),
    ('summer 32x32.png', 'winter-clean',     '/tilesets/mana-seed/winter-forest/winter sheets/winter (clean) 32x32.png', true),
    ('summer 32x32.png', 'winter-leaves',    '/tilesets/mana-seed/winter-forest/winter sheets/winter (leaves) 32x32.png', true),
    ('summer 32x32.png', 'winter-snowy-top', '/tilesets/mana-seed/winter-forest/winter sheets/winter (snowy top) 32x32.png', true),
    ('summer 32x32.png', 'winter-snowy',     '/tilesets/mana-seed/winter-forest/winter sheets/winter (snowy) 32x32.png', true),

    ('summer 48x32.png', 'autumn',           '/tilesets/mana-seed/autumn-forest/autumn sheets/autumn 48x32.png', false),
    ('summer 48x32.png', 'winter-clean',     '/tilesets/mana-seed/winter-forest/winter sheets/winter (clean) 48x32.png', true),
    ('summer 48x32.png', 'winter-leaves',    '/tilesets/mana-seed/winter-forest/winter sheets/winter (leaves) 48x32.png', true),
    ('summer 48x32.png', 'winter-snowy-top', '/tilesets/mana-seed/winter-forest/winter sheets/winter (snowy top) 48x32.png', true),
    ('summer 48x32.png', 'winter-snowy',     '/tilesets/mana-seed/winter-forest/winter sheets/winter (snowy) 48x32.png', true),

    ('summer 16x16.png', 'autumn',       '/tilesets/mana-seed/autumn-forest/autumn sheets/autumn 16x16.png', false),
    ('summer 16x16.png', 'winter-clean', '/tilesets/mana-seed/winter-forest/winter sheets/winter (clean) 16x16.png', true),
    ('summer 16x16.png', 'winter-snowy', '/tilesets/mana-seed/winter-forest/winter sheets/winter (snowy) 16x16.png', true),

    ('summer 16x32.png', 'autumn',       '/tilesets/mana-seed/autumn-forest/autumn sheets/autumn 16x32.png', false),
    ('summer 16x32.png', 'winter-clean', '/tilesets/mana-seed/winter-forest/winter sheets/winter (clean) 16x32.png', true),
    ('summer 16x32.png', 'winter-snowy', '/tilesets/mana-seed/winter-forest/winter sheets/winter (snowy) 16x32.png', true);

-- One row per state to add: its source state, its new name, look, sheet and
-- (for the apple's row 2) a src override.
CREATE TEMP TABLE llm849_new (
    asset_id     uuid NOT NULL,
    src_state_id integer NOT NULL,
    state        text NOT NULL,
    look         text NOT NULL,
    sheet        text NOT NULL,
    src_x        integer NULL,
    src_y        integer NULL
) ON COMMIT DROP;

INSERT INTO llm849_new (asset_id, src_state_id, state, look, sheet)
SELECT s.asset_id, s.id,
       CASE WHEN s.state = 'default' THEN l.look ELSE l.look || '-' || s.state END,
       l.look, l.sheet
  FROM asset_state s
  JOIN llm849_look l
    ON s.sheet = '/tilesets/mana-seed/summer-forest/summer sheets/' || l.summer_sheet
 WHERE NOT (l.winter AND s.asset_id IN ('630909ca-df4f-43ac-9fc4-5192ca44da73',   -- Blueberry Bush
                                        'db4b428c-9ab6-4457-85fb-3f85fe86c946',   -- Raspberry Bush
                                        'bad3e45f-3649-4cf2-b9b2-cda95b334113'))  -- Sage Bush
   AND NOT EXISTS (SELECT 1 FROM asset_state_tag t
                    WHERE t.state_id = s.id AND t.tag LIKE 'season-%');

-- Apple Tree: autumn is row 2 of its own sheet.
INSERT INTO llm849_new (asset_id, src_state_id, state, look, sheet, src_x, src_y)
SELECT s.asset_id, s.id, 'autumn-' || s.state, 'autumn', s.sheet,
       CASE s.state WHEN 'bare' THEN 144 ELSE 192 END, 128
  FROM asset_state s
 WHERE s.asset_id = '019e5f00-c401-7a10-9e00-000000000623'
   AND s.state IN ('bare', 'berries');

-- Tiny Swords Tree 3 / Tree 4: the built season sheets beside the summer one.
INSERT INTO llm849_new (asset_id, src_state_id, state, look, sheet)
SELECT s.asset_id, s.id, v.look, v.look,
       replace(s.sheet, '.png', '-' || v.look || '.png')
  FROM asset_state s
 CROSS JOIN (VALUES ('autumn'), ('winter'), ('winter-snowy')) AS v(look)
 WHERE s.asset_id IN ('f4376700-2b88-4a0e-8d4b-700063068c04',   -- Tree 3
                      'a726d5fd-a740-4f39-b9f8-dc9651609b09')   -- Tree 4
   AND s.state = 'default';

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT n.asset_id, n.state, n.sheet, COALESCE(n.src_x, s.src_x), COALESCE(n.src_y, s.src_y),
       s.src_w, s.src_h, s.frame_count, s.frame_rate
  FROM llm849_new n
  JOIN asset_state s ON s.id = n.src_state_id
 WHERE NOT EXISTS (SELECT 1 FROM asset_state d
                    WHERE d.asset_id = n.asset_id AND d.state = n.state);

-- The look tag.
INSERT INTO asset_state_tag (state_id, tag)
SELECT d.id, 'season-' || n.look
  FROM llm849_new n
  JOIN asset_state d ON d.asset_id = n.asset_id AND d.state = n.state
ON CONFLICT DO NOTHING;

-- The source state's own tags (the bushes' and the apple's berries / bare).
INSERT INTO asset_state_tag (state_id, tag)
SELECT d.id, t.tag
  FROM llm849_new n
  JOIN asset_state d ON d.asset_id = n.asset_id AND d.state = n.state
  JOIN asset_state_tag t ON t.state_id = n.src_state_id
ON CONFLICT DO NOTHING;

-- Validate loud, only where the catalog exists (a schema-only harness has none).
DO $$
DECLARE
    missing integer;
BEGIN
    IF EXISTS (SELECT 1 FROM asset WHERE id = '630909ca-df4f-43ac-9fc4-5192ca44da73') THEN
        -- Every summer-forest state has its autumn row.
        SELECT count(*) INTO missing
          FROM asset_state s
         WHERE s.sheet LIKE '/tilesets/mana-seed/summer-forest/summer sheets/%'
           AND NOT EXISTS (SELECT 1 FROM asset_state d
                             JOIN asset_state_tag t ON t.state_id = d.id
                            WHERE d.asset_id = s.asset_id AND t.tag LIKE 'season-autumn%');
        IF missing > 0 THEN
            RAISE EXCEPTION 'LLM-849: % summer-forest states have no autumn state', missing;
        END IF;
        -- The bushes have no winter rows; their stock tags came across.
        IF EXISTS (SELECT 1 FROM asset_state d
                     JOIN asset_state_tag t ON t.state_id = d.id
                    WHERE d.asset_id IN ('630909ca-df4f-43ac-9fc4-5192ca44da73',
                                         'db4b428c-9ab6-4457-85fb-3f85fe86c946',
                                         'bad3e45f-3649-4cf2-b9b2-cda95b334113',
                                         '019e5f00-c401-7a10-9e00-000000000623')
                      AND t.tag LIKE 'season-winter%') THEN
            RAISE EXCEPTION 'LLM-849: a bush or the apple tree got a winter state';
        END IF;
        IF (SELECT count(*) FROM asset_state d
              JOIN asset_state_tag t ON t.state_id = d.id
             WHERE d.state IN ('autumn-berries', 'autumn-bare')
               AND t.tag IN ('berries', 'bare')) <> 6 THEN
            RAISE EXCEPTION 'LLM-849: expected 6 stock-tagged autumn states (blueberry, raspberry, apple × berries/bare)';
        END IF;
        IF (SELECT count(*) FROM asset_state
             WHERE asset_id IN ('f4376700-2b88-4a0e-8d4b-700063068c04', 'a726d5fd-a740-4f39-b9f8-dc9651609b09')
               AND state IN ('autumn', 'winter', 'winter-snowy') AND frame_count = 8) <> 6 THEN
            RAISE EXCEPTION 'LLM-849: Tree 3 / Tree 4 season states missing or not 8-frame';
        END IF;
    END IF;
END $$;

COMMIT;
