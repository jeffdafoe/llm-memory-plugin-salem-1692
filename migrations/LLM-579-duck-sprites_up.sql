-- LLM-579: duck sprite catalog — the Mana Seed "Animated Livestock 4.0" duck
-- in all eight plumages, as placeable NPC sprites with a waterfowl behavior
-- marker. Three schema touches + catalog rows:
--
--   (0) npc_sprite.behaviors — a jsonb array of behavior slugs the engine keys
--       autonomous movement on. The waterfowl wander (swim the lake, potter the
--       shore) is engine-driven off the SPRITE, not a per-actor attribute:
--       dropping a duck into the pond via the ordinary editor NPC placement is
--       the whole authoring flow, with no follow-up attribute grant. An array
--       (not a boolean column) per the project convention — the next behavior
--       ('flocking', 'skittish', ...) is a value, not a column.
--
--   (1) Extend the npc_sprite_animation animation CHECK with 'swim'. The
--       client plays direction+"_swim" (single-frame float pose) whenever a
--       waterfowl actor's tile is water; idle/walk stay untouched for every
--       existing sprite.
--
--   (2) 8 npc_sprite rows + 12 animations each (idle/walk/swim x 4 directions)
--       under a new 'mana-seed-livestock' tileset_pack. The sheets are the
--       DERIVED 192x352 layout produced by llm-memory-village-tiles
--       tools/extend-duck-sheets.ps1 (swim poses re-laid-out as rows 7-10,
--       because the animation model can only address frames starting at
--       column 0; west is a mirror the pack doesn't ship). The ripple/shadow
--       decal cells stay at their source row-4 coordinates — the client reads
--       them from fixed positions documented beside its decal builder.
--
-- npc_sprite / npc_sprite_animation / tileset_pack are boot-loaded reference
-- data (no checkpoint path). Fixed UUIDs so re-runs and the down migration are
-- deterministic; every insert is ON CONFLICT DO NOTHING / guarded, so the
-- schema-only fresh DB the pg integration suite replays takes this cleanly.
-- The PNGs live in the operator-managed VPS persistent tilesets dir
-- (/var/www/llm-memory-salem-1692/tilesets/mana-seed/livestock/), shipped
-- separately — a missing sheet renders the editor-picker entry blank but
-- breaks nothing.

BEGIN;

ALTER TABLE public.npc_sprite
    ADD COLUMN IF NOT EXISTS behaviors jsonb NOT NULL DEFAULT '[]'::jsonb;

ALTER TABLE public.npc_sprite_animation
    DROP CONSTRAINT IF EXISTS npc_sprite_animation_animation_check;
ALTER TABLE public.npc_sprite_animation
    ADD CONSTRAINT npc_sprite_animation_animation_check
    CHECK (((animation)::text = ANY ((ARRAY[
        'idle'::character varying,
        'walk'::character varying,
        'swim'::character varying])::text[])));

INSERT INTO public.tileset_pack (id, name, url)
VALUES ('mana-seed-livestock', 'Animated Livestock',
        'https://seliel-the-shaper.itch.io/animated-livestock')
ON CONFLICT (id) DO NOTHING;

-- The eight plumages. Deterministic UUIDs (LLM-579 namespace, hand-minted).
INSERT INTO public.npc_sprite (id, name, sheet, frame_width, frame_height, pack_id, behaviors)
VALUES
    ('579d0c01-0000-4000-8000-000000000001', 'Duck (mallard)',    '/tilesets/mana-seed/livestock/livestock_duck_v01.png', 32, 32, 'mana-seed-livestock', '["waterfowl"]'),
    ('579d0c02-0000-4000-8000-000000000002', 'Duck (brown pied)', '/tilesets/mana-seed/livestock/livestock_duck_v02.png', 32, 32, 'mana-seed-livestock', '["waterfowl"]'),
    ('579d0c03-0000-4000-8000-000000000003', 'Duck (pale grey)',  '/tilesets/mana-seed/livestock/livestock_duck_v03.png', 32, 32, 'mana-seed-livestock', '["waterfowl"]'),
    ('579d0c04-0000-4000-8000-000000000004', 'Duck (grey)',       '/tilesets/mana-seed/livestock/livestock_duck_v04.png', 32, 32, 'mana-seed-livestock', '["waterfowl"]'),
    ('579d0c05-0000-4000-8000-000000000005', 'Duck (silver)',     '/tilesets/mana-seed/livestock/livestock_duck_v05.png', 32, 32, 'mana-seed-livestock', '["waterfowl"]'),
    ('579d0c06-0000-4000-8000-000000000006', 'Duck (yellow)',     '/tilesets/mana-seed/livestock/livestock_duck_v06.png', 32, 32, 'mana-seed-livestock', '["waterfowl"]'),
    ('579d0c07-0000-4000-8000-000000000007', 'Duck (fawn)',       '/tilesets/mana-seed/livestock/livestock_duck_v07.png', 32, 32, 'mana-seed-livestock', '["waterfowl"]'),
    ('579d0c08-0000-4000-8000-000000000008', 'Duck (brown)',      '/tilesets/mana-seed/livestock/livestock_duck_v08.png', 32, 32, 'mana-seed-livestock', '["waterfowl"]')
ON CONFLICT (id) DO NOTHING;

-- 12 animations per sprite. Sheet rows (derived layout):
--   0=south 1=north 2=east 3=west  — 6-frame walk cycles; frame 0 doubles as idle
--   7=south 8=north 9=east 10=west — 1-frame swim (float) poses
INSERT INTO public.npc_sprite_animation (sprite_id, direction, animation, row_index, frame_count, frame_rate)
SELECT s.id, a.direction, a.animation, a.row_index, a.frame_count, a.frame_rate
FROM public.npc_sprite s
CROSS JOIN (VALUES
    ('south', 'idle',  0, 1, 1.0),
    ('north', 'idle',  1, 1, 1.0),
    ('east',  'idle',  2, 1, 1.0),
    ('west',  'idle',  3, 1, 1.0),
    ('south', 'walk',  0, 6, 8.0),
    ('north', 'walk',  1, 6, 8.0),
    ('east',  'walk',  2, 6, 8.0),
    ('west',  'walk',  3, 6, 8.0),
    ('south', 'swim',  7, 1, 1.0),
    ('north', 'swim',  8, 1, 1.0),
    ('east',  'swim',  9, 1, 1.0),
    ('west',  'swim', 10, 1, 1.0)
) AS a(direction, animation, row_index, frame_count, frame_rate)
WHERE s.pack_id = 'mana-seed-livestock'
  AND NOT EXISTS (
      SELECT 1 FROM public.npc_sprite_animation existing
      WHERE existing.sprite_id = s.id
        AND existing.direction = a.direction
        AND existing.animation = a.animation
  );

COMMIT;
