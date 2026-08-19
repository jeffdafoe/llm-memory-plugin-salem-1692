-- LLM-639: cattle sprites + the fence-gate contract.
--
-- Two halves:
--
-- (1) EIGHT CATTLE SPRITES off the Mana Seed "Animated Livestock 4.0" pack
--     (tileset_pack mana-seed-livestock, created by LLM-579 for the ducks).
--     Cattle sheets are 512x512 with 64x64 cells and — unlike the ducks —
--     need NO re-layout: rows 0-3 are 6-frame walk cycles facing
--     south/north/east/west (the same row-facing convention as the derived
--     duck sheets), rows 4-7 are 5-frame GRAZE loops in the same order. The
--     graze rows are authored as the idle animation, so a standing cow
--     visibly eats — which is also why the grazer wander's dwells are long.
--
--     behaviors = ["grazer", "ambient"]: grazer drives the wander and the
--     gate-blocked walk grid (engine/sim/grazer.go); ambient keeps every leg
--     out of agent_action_log and the atmosphere prompts (the LLM-593 split —
--     omitting it would write thousands of walk rows a day, the LLM-593
--     defect shape).
--
--     render_scale 1.0: a 64px cow drawn at 1.0 spans ~2 tiles beside a
--     2.0-scale villager. A pure client draw hint (the engine never reads
--     it), live-tunable per sprite.
--
-- (2) THE FENCE-GATE CONTRACT on the existing "Ranch Fence Gate" asset
--     (e2c34e96-4f59-…, seeded with the original catalog, zero placements):
--     an asset whose state carries the asset_state_tag 'fence-gate' is a
--     livestock barrier — its placements' FOOTPRINT tiles are stamped
--     impassable on the grazer walk grid only. For that to let PEOPLE
--     through, the asset must stop being an obstacle, and the footprint is
--     widened to 2 tiles (left 0 / right 1) to match the art's opening; both
--     are editor-tunable afterwards (asset-geometry routes). The tag, not
--     the asset id, is the contract — a future picket gate is authored the
--     same way with no code change.
--
--     GUARDED, NOT ASSERTED: the gate asset is live-only data (the original
--     catalog seed predates the migration baseline, which is schema-only),
--     so on the pg integration harness's fresh replay these statements
--     match zero rows and that is fine — gates are data-driven and a world
--     without one simply has no livestock barriers.
--
-- npc_sprite / npc_sprite_animation / asset / asset_state_tag are boot-loaded
-- reference data (no checkpoint path) — no engine stop needed; the deploy's
-- restart loads them. Fixed sprite UUIDs so re-runs and the down are
-- deterministic; every insert is ON CONFLICT / NOT EXISTS guarded.
--
-- THE SHEETS TRAVEL BY SCP, NOT BY DEPLOY (gitignored): eight PNGs renamed
-- hyphenated-lowercase into
-- /var/www/llm-memory-salem-1692/tilesets/mana-seed/livestock/ (owner
-- www-data). A missing sheet renders a blank editor-picker entry, breaks
-- nothing.

BEGIN;

-- Pack row exists on prod (LLM-579) and on a fresh replay (same migration).
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM tileset_pack WHERE id = 'mana-seed-livestock') THEN
        RAISE EXCEPTION
            'LLM-639: tileset_pack mana-seed-livestock is missing; LLM-579 should have created it';
    END IF;
END $$;

-- The herd. Deterministic UUIDs (LLM-639 namespace, hand-minted). Colourway
-- names from the pack's v-suffixes: v01 white, v02 golden, v03 brown,
-- v04 russet, v06 grey. Bull and heifer are distinct silhouettes (horns /
-- smaller frame), so they get their own entries rather than being colourways.
INSERT INTO public.npc_sprite (id, name, sheet, frame_width, frame_height, pack_id, behaviors, render_scale)
VALUES
    ('639d0c01-0000-4000-8000-000000000001', 'Cow (white)',      '/tilesets/mana-seed/livestock/livestock-cattle-cow-a-v01.png',    64, 64, 'mana-seed-livestock', '["grazer", "ambient"]', 1.0),
    ('639d0c02-0000-4000-8000-000000000002', 'Cow (golden)',     '/tilesets/mana-seed/livestock/livestock-cattle-cow-a-v02.png',    64, 64, 'mana-seed-livestock', '["grazer", "ambient"]', 1.0),
    ('639d0c03-0000-4000-8000-000000000003', 'Cow (brown)',      '/tilesets/mana-seed/livestock/livestock-cattle-cow-a-v03.png',    64, 64, 'mana-seed-livestock', '["grazer", "ambient"]', 1.0),
    ('639d0c04-0000-4000-8000-000000000004', 'Cow (grey)',       '/tilesets/mana-seed/livestock/livestock-cattle-cow-a-v06.png',    64, 64, 'mana-seed-livestock', '["grazer", "ambient"]', 1.0),
    ('639d0c05-0000-4000-8000-000000000005', 'Bull (white)',     '/tilesets/mana-seed/livestock/livestock-cattle-bull-a-v01.png',   64, 64, 'mana-seed-livestock', '["grazer", "ambient"]', 1.0),
    ('639d0c06-0000-4000-8000-000000000006', 'Bull (brown)',     '/tilesets/mana-seed/livestock/livestock-cattle-bull-a-v03.png',   64, 64, 'mana-seed-livestock', '["grazer", "ambient"]', 1.0),
    ('639d0c07-0000-4000-8000-000000000007', 'Heifer (golden)',  '/tilesets/mana-seed/livestock/livestock-cattle-heifer-a-v02.png', 64, 64, 'mana-seed-livestock', '["grazer", "ambient"]', 1.0),
    ('639d0c08-0000-4000-8000-000000000008', 'Heifer (russet)',  '/tilesets/mana-seed/livestock/livestock-cattle-heifer-a-v04.png', 64, 64, 'mana-seed-livestock', '["grazer", "ambient"]', 1.0)
ON CONFLICT (id) DO NOTHING;

-- 8 animations per sprite: walk rows 0-3 (6 frames), graze-as-idle rows 4-7
-- (5 frames, slow — it is eating, not flickering). Scoped to the cattle ids:
-- the pack also holds the ducks, whose rows this must not touch.
INSERT INTO public.npc_sprite_animation (sprite_id, direction, animation, row_index, frame_count, frame_rate)
SELECT s.id, a.direction, a.animation, a.row_index, a.frame_count, a.frame_rate
FROM public.npc_sprite s
CROSS JOIN (VALUES
    ('south', 'walk', 0, 6, 7.0),
    ('north', 'walk', 1, 6, 7.0),
    ('east',  'walk', 2, 6, 7.0),
    ('west',  'walk', 3, 6, 7.0),
    ('south', 'idle', 4, 5, 3.0),
    ('north', 'idle', 5, 5, 3.0),
    ('east',  'idle', 6, 5, 3.0),
    ('west',  'idle', 7, 5, 3.0)
) AS a(direction, animation, row_index, frame_count, frame_rate)
WHERE s.id IN (
    '639d0c01-0000-4000-8000-000000000001', '639d0c02-0000-4000-8000-000000000002',
    '639d0c03-0000-4000-8000-000000000003', '639d0c04-0000-4000-8000-000000000004',
    '639d0c05-0000-4000-8000-000000000005', '639d0c06-0000-4000-8000-000000000006',
    '639d0c07-0000-4000-8000-000000000007', '639d0c08-0000-4000-8000-000000000008')
  AND NOT EXISTS (
      SELECT 1 FROM public.npc_sprite_animation existing
      WHERE existing.sprite_id = s.id
        AND existing.direction = a.direction
        AND existing.animation = a.animation
  );

-- (2) Ranch Fence Gate becomes a livestock barrier: not an obstacle (people
-- pass), 2-tile footprint (the grazer grid stamps it), tagged fence-gate.
UPDATE public.asset
   SET is_obstacle = false,
       footprint_left = 0, footprint_right = 1,
       footprint_top = 0, footprint_bottom = 0
 WHERE id = 'e2c34e96-14ef-487c-a6af-d2526d4c4526';

INSERT INTO public.asset_state_tag (state_id, tag)
SELECT s.id, 'fence-gate'
  FROM public.asset_state s
 WHERE s.asset_id = 'e2c34e96-14ef-487c-a6af-d2526d4c4526'
   AND NOT EXISTS (
       SELECT 1 FROM public.asset_state_tag t
        WHERE t.state_id = s.id AND t.tag = 'fence-gate');

COMMIT;
