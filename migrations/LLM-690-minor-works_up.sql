-- LLM-690: the damaged states minor works flip to.
--
-- A minor work is a state flip on an existing placement (engine/sim/damage_minor.go).
-- The asset carries the broken states, tagged:
--
--   minor-of-<state>    this state is the broken form of <state>, and mends back to it
--   minor-form-<form>   the SITE state — picks the mini-game and the wording
--   minor-left-<state>  / minor-right-<state>  the site's edges: the neighbour
--                       placement one tile left / right flips to <state>
--
-- THE RANCH FENCE. The Mana Seed sheet draws a break across three 16x16 cells
-- in columns 4-6 — the middle post gone and the rails sagging into the posts
-- either side — four ways, one per row (LLM-637 held these back for this).
-- So a break is one 'h' segment (the site, column 5) plus its two 'h'
-- neighbours (columns 4 and 6). Composited and checked against the live 'h'
-- cell before this was written.
--
-- THE SIGNPOST AND THE CRATE are single placements. The Wood Sign Post's arm
-- hangs crooked (village signposts 48x64, cell (2,0): the default's arm angled
-- down). The pack has no smashed crate, so the crate's lid is knocked loose
-- (village accessories 32x32, cell (1,1): the open crate beside the default
-- (0,1)). Both assets were seeded by hand, not by a migration, so their rows
-- are inserted only where the asset exists (the pg harness replays migrations
-- on a schema-only baseline).
--
-- asset / asset_state / asset_state_tag are REFERENCE data: load-only, never
-- checkpoint-clobbered, so this needs no engine stop. The catalog is
-- boot-loaded; the deploy restarts the engine.

BEGIN;

CREATE TEMP TABLE llm690_state (
    asset_id uuid, state text, sheet text,
    src_x int, src_y int, src_w int, src_h int
) ON COMMIT DROP;

CREATE TEMP TABLE llm690_tag (asset_id uuid, state text, tag text) ON COMMIT DROP;

-- Fence: per break n (sheet row n-1), the site and its two edges.
INSERT INTO llm690_state
SELECT '019e5f00-c401-7a10-9e00-000000637001', s.state,
       '/tilesets/mana-seed/fences-walls/ranch style fence 16x16.png',
       s.col * 16, (n - 1) * 16, 16, 16
  FROM generate_series(1, 4) AS n,
       LATERAL (VALUES ('h-broken-' || n || '-l', 4),
                       ('h-broken-' || n,         5),
                       ('h-broken-' || n || '-r', 6)) AS s(state, col);

INSERT INTO llm690_tag
SELECT '019e5f00-c401-7a10-9e00-000000637001', t.state, t.tag
  FROM generate_series(1, 4) AS n,
       LATERAL (VALUES ('h-broken-' || n,         'minor-of-h'),
                       ('h-broken-' || n,         'minor-form-fence'),
                       ('h-broken-' || n,         'minor-left-h-broken-' || n || '-l'),
                       ('h-broken-' || n,         'minor-right-h-broken-' || n || '-r'),
                       ('h-broken-' || n || '-l', 'minor-of-h'),
                       ('h-broken-' || n || '-r', 'minor-of-h')) AS t(state, tag);

-- Wood Sign Post and Crate.
INSERT INTO llm690_state VALUES
    ('796d76b4-9a3b-4541-9852-98d389c1906a', 'crooked',
     '/tilesets/mana-seed/village-accessories/village signposts 48x64.png', 96, 0, 48, 64),
    ('7bf7022c-c354-4333-a607-bb42650d2666', 'lid-loose',
     '/tilesets/mana-seed/village-accessories/village accessories 32x32.png', 32, 32, 32, 32);

INSERT INTO llm690_tag VALUES
    ('796d76b4-9a3b-4541-9852-98d389c1906a', 'crooked',   'minor-of-default'),
    ('796d76b4-9a3b-4541-9852-98d389c1906a', 'crooked',   'minor-form-signpost'),
    ('7bf7022c-c354-4333-a607-bb42650d2666', 'lid-loose', 'minor-of-default'),
    ('7bf7022c-c354-4333-a607-bb42650d2666', 'lid-loose', 'minor-form-crate');

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT s.asset_id, s.state, s.sheet, s.src_x, s.src_y, s.src_w, s.src_h, 1, 0
  FROM llm690_state s
 WHERE EXISTS (SELECT 1 FROM asset a WHERE a.id = s.asset_id);

INSERT INTO asset_state_tag (state_id, tag)
SELECT st.id, t.tag
  FROM llm690_tag t
  JOIN asset_state st ON st.asset_id = t.asset_id AND st.state = t.state;

-- Every state and tag landed for every asset that exists.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM llm690_state s
                WHERE EXISTS (SELECT 1 FROM asset a WHERE a.id = s.asset_id)
                  AND NOT EXISTS (SELECT 1 FROM asset_state st
                                   WHERE st.asset_id = s.asset_id AND st.state = s.state
                                     AND st.sheet = s.sheet AND st.src_x = s.src_x AND st.src_y = s.src_y
                                     AND st.src_w = s.src_w AND st.src_h = s.src_h)) THEN
        RAISE EXCEPTION 'LLM-690: a minor-work state is missing or has the wrong frame';
    END IF;
    IF EXISTS (SELECT 1 FROM llm690_tag t
                WHERE EXISTS (SELECT 1 FROM asset a WHERE a.id = t.asset_id)
                  AND NOT EXISTS (SELECT 1 FROM asset_state st
                                    JOIN asset_state_tag g ON g.state_id = st.id
                                   WHERE st.asset_id = t.asset_id AND st.state = t.state AND g.tag = t.tag)) THEN
        RAISE EXCEPTION 'LLM-690: a minor-work state tag is missing';
    END IF;
END $$;

COMMIT;
