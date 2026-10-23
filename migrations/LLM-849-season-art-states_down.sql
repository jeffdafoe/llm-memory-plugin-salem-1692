-- LLM-849 down: remove the season art states this migration added, and only
-- those.
--
-- The manifest is rebuilt the way the up migration built it — the look names per
-- summer-forest sheet (bushes without the winter looks), the apple tree's two
-- autumn states, Tree 3 / Tree 4's three — so a season state another migration
-- adds is left alone. Objects standing in a removed state go back to their
-- asset's default state first; the engine's one-minute sweep then restores a
-- bush's berries / bare from its stock. Tags and lights go with their states
-- (ON DELETE CASCADE).

BEGIN;

CREATE TEMP TABLE llm849_down (
    asset_id uuid NOT NULL,
    state    text NOT NULL
) ON COMMIT DROP;

INSERT INTO llm849_down (asset_id, state)
SELECT s.asset_id,
       CASE WHEN s.state = 'default' THEN l.look ELSE l.look || '-' || s.state END
  FROM asset_state s
  JOIN (VALUES
        ('summer trees 80x112.png', 'autumn-clean', false), ('summer trees 80x112.png', 'autumn-leaves', false),
        ('summer trees 80x112.png', 'autumn-bare', false), ('summer trees 80x112.png', 'winter-clean', true),
        ('summer trees 80x112.png', 'winter-leaves', true), ('summer trees 80x112.png', 'winter-snowy-top', true),
        ('summer trees 80x112.png', 'winter-snowy', true),
        ('summer 32x32.png', 'autumn', false), ('summer 32x32.png', 'winter-clean', true),
        ('summer 32x32.png', 'winter-leaves', true), ('summer 32x32.png', 'winter-snowy-top', true),
        ('summer 32x32.png', 'winter-snowy', true),
        ('summer 48x32.png', 'autumn', false), ('summer 48x32.png', 'winter-clean', true),
        ('summer 48x32.png', 'winter-leaves', true), ('summer 48x32.png', 'winter-snowy-top', true),
        ('summer 48x32.png', 'winter-snowy', true),
        ('summer 16x16.png', 'autumn', false), ('summer 16x16.png', 'winter-clean', true),
        ('summer 16x16.png', 'winter-snowy', true),
        ('summer 16x32.png', 'autumn', false), ('summer 16x32.png', 'winter-clean', true),
        ('summer 16x32.png', 'winter-snowy', true)
       ) AS l(summer_sheet, look, winter)
    ON s.sheet = '/tilesets/mana-seed/summer-forest/summer sheets/' || l.summer_sheet
 WHERE NOT (l.winter AND s.asset_id IN ('630909ca-df4f-43ac-9fc4-5192ca44da73',   -- Blueberry Bush
                                        'db4b428c-9ab6-4457-85fb-3f85fe86c946',   -- Raspberry Bush
                                        'bad3e45f-3649-4cf2-b9b2-cda95b334113')); -- Sage Bush

INSERT INTO llm849_down (asset_id, state) VALUES
    ('019e5f00-c401-7a10-9e00-000000000623', 'autumn-bare'),     -- Apple Tree
    ('019e5f00-c401-7a10-9e00-000000000623', 'autumn-berries'),
    ('f4376700-2b88-4a0e-8d4b-700063068c04', 'autumn'),          -- Tree 3
    ('f4376700-2b88-4a0e-8d4b-700063068c04', 'winter'),
    ('f4376700-2b88-4a0e-8d4b-700063068c04', 'winter-snowy'),
    ('a726d5fd-a740-4f39-b9f8-dc9651609b09', 'autumn'),          -- Tree 4
    ('a726d5fd-a740-4f39-b9f8-dc9651609b09', 'winter'),
    ('a726d5fd-a740-4f39-b9f8-dc9651609b09', 'winter-snowy');

UPDATE village_object o
   SET current_state = a.default_state
  FROM asset a, llm849_down m
 WHERE a.id = o.asset_id
   AND m.asset_id = o.asset_id AND m.state = o.current_state;

DELETE FROM asset_state s
 USING llm849_down m
 WHERE s.asset_id = m.asset_id AND s.state = m.state;

COMMIT;
