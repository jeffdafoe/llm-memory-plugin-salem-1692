-- LLM-689: the Tiny Swords sheep, and animals named for what they are.
--
-- Two halves:
--
-- (1) THE SHEEP SPRITE, a grazer like the cattle (engine/sim/grazer.go,
--     LLM-639). The sheet is DERIVED by llm-memory-village-tiles
--     tools/build-sheep-sheet.ps1 from the Tiny Swords (Free Pack) animated
--     sheep: the art halved to 32x32 cells (at native size a sheep is ~1.5x a
--     Mana Seed cow, and the client's nearest filtering rules out a 0.5 draw
--     scale), west baked as a mirror (the client cannot flip; the duck
--     precedent). The cattle row convention:
--       rows 0-3  walk  south / north / east / west   4 frames
--       rows 4-7  graze south / north / east / west  12 frames — the idle
--     The art is side-view only, so south reuses east and north the mirror.
--     behaviors ["grazer", "ambient"] exactly as the cattle; render_scale 1.0.
--
--     The tiny-swords tileset pack row exists on prod (created with the
--     original Tiny Swords catalog); ON CONFLICT keeps this a no-op there and
--     creates it on a fresh replay.
--
-- (2) ANIMALS NAMED FOR THEIR SPECIES. An editor placement without a name used
--     to default to "Villager" / "Villager N" whatever the sprite, so the pen
--     animals read as villagers on every roster ("Villager 3" is a rooster).
--     The engine now names an unnamed animal for its species (sim.CreateNPC);
--     this renames the animals already placed under the old default: an actor
--     whose sprite is an animal (behaviors grazer or waterfowl — the
--     sim.Sprite.IsAnimal predicate) and whose name is exactly "Villager" or
--     "Villager N" takes its sprite's name up to the colourway ("Cow (grey)"
--     -> "Cow"), numbered per species in the old name order ("Cow", "Cow 2").
--     A name someone chose (the ducks are all "Duck") is left alone. Decorative
--     actors may share a name (LLM-586), so no uniqueness pass is needed.
--
--     actor is engine-owned, but deploy.sh runs stop -> migrate -> start with
--     a confirmed checkpoint before the migration, so nothing overwrites this.
--     On a fresh replay there are no actors and it matches zero rows.
--
-- THE SHEET TRAVELS BY SCP, NOT BY DEPLOY: tilesets/tiny-swords/livestock/
-- sheep.png into /var/www/llm-memory-salem-1692/tilesets/tiny-swords/livestock/
-- (owner www-data). A missing sheet renders a blank editor-picker entry and
-- breaks nothing.
--
-- Rerun-safe: every insert is ON CONFLICT / NOT EXISTS guarded, and a second
-- rename pass finds no "Villager" animals.

BEGIN;

INSERT INTO public.tileset_pack (id, name, url)
VALUES ('tiny-swords', 'Tiny Swords', 'https://pixelfrog-assets.itch.io/tiny-swords')
ON CONFLICT (id) DO NOTHING;

INSERT INTO public.npc_sprite (id, name, sheet, frame_width, frame_height, pack_id, behaviors, render_scale)
VALUES ('689d0c01-0000-4000-8000-000000000001', 'Sheep', '/tilesets/tiny-swords/livestock/sheep.png',
        32, 32, 'tiny-swords', '["grazer", "ambient"]', 1.0)
ON CONFLICT (id) DO NOTHING;

INSERT INTO public.npc_sprite_animation (sprite_id, direction, animation, row_index, frame_count, frame_rate)
SELECT '689d0c01-0000-4000-8000-000000000001', a.direction, a.animation, a.row_index, a.frame_count, a.frame_rate
FROM (VALUES
    ('south', 'walk', 0, 4, 6.0),
    ('north', 'walk', 1, 4, 6.0),
    ('east',  'walk', 2, 4, 6.0),
    ('west',  'walk', 3, 4, 6.0),
    ('south', 'idle', 4, 12, 5.0),
    ('north', 'idle', 5, 12, 5.0),
    ('east',  'idle', 6, 12, 5.0),
    ('west',  'idle', 7, 12, 5.0)
) AS a(direction, animation, row_index, frame_count, frame_rate)
WHERE NOT EXISTS (
    SELECT 1 FROM public.npc_sprite_animation existing
     WHERE existing.sprite_id = '689d0c01-0000-4000-8000-000000000001'
       AND existing.direction = a.direction
       AND existing.animation = a.animation);

WITH animals AS (
    SELECT a.id,
           split_part(s.name, ' (', 1) AS species,
           row_number() OVER (
               PARTITION BY split_part(s.name, ' (', 1)
               ORDER BY coalesce(nullif(substring(a.display_name FROM '^Villager ([0-9]+)$'), ''), '1')::int, a.id
           ) AS n
      FROM public.actor a
      JOIN public.npc_sprite s ON s.id = a.sprite_id
     WHERE (s.behaviors ? 'grazer' OR s.behaviors ? 'waterfowl')
       AND a.display_name ~ '^Villager( [0-9]+)?$'
)
UPDATE public.actor a
   SET display_name = CASE WHEN animals.n = 1 THEN animals.species ELSE animals.species || ' ' || animals.n END
  FROM animals
 WHERE a.id = animals.id;

DO $$
DECLARE left_over int;
BEGIN
    SELECT count(*) INTO left_over
      FROM public.actor a
      JOIN public.npc_sprite s ON s.id = a.sprite_id
     WHERE (s.behaviors ? 'grazer' OR s.behaviors ? 'waterfowl')
       AND a.display_name ~ '^Villager( [0-9]+)?$';
    IF left_over > 0 THEN
        RAISE EXCEPTION 'LLM-689: % animal(s) still named Villager after the rename', left_over;
    END IF;
END $$;

COMMIT;
