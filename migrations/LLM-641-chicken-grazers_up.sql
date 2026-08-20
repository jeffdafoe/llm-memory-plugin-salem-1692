-- LLM-641: six chicken sprites off the Mana Seed "Animated Livestock 4.0"
-- pack (tileset_pack mana-seed-livestock) — pure data on the grazer slug,
-- the follow-up LLM-639 anticipated. No engine change and no gate work:
-- the fence-gate contract already exists, so the chickens simply inherit it.
--
-- Chicken sheets are 512x512 with 64x64 cells in the cattle layout: rows 0-3
-- are 6-frame walk cycles facing south/north/east/west, rows 4-7 are PECK
-- loops in the same order — but 3 frames, not the cattle's 5. The peck rows
-- are authored as the idle animation, so a standing bird visibly forages,
-- same trick as the grazing cows.
--
-- behaviors = ["grazer", "ambient"]: grazer drives the wander and the
-- gate-blocked walk grid (engine/sim/grazer.go), ambient keeps every leg out
-- of agent_action_log and the atmosphere prompts (the LLM-593 split).
-- Stock grazer tuning (amble 5 / roam 12 / half-speed walk) is a deliberate
-- LLM-641 decision — per-species tuning is a possible follow-up.
--
-- render_scale 1.0, matching the cattle: a pure client draw hint, live-tunable
-- per sprite. The birds are drawn small within their 64px cells, so they read
-- chicken-sized beside the cows without a special scale.
--
-- Colourways (Jeff's picks): the pack's letter codes are body/pattern
-- variants — A-prefix silhouettes read as hens, B-prefix as combed roosters —
-- and the v-suffix is the palette.
--
-- npc_sprite / npc_sprite_animation are boot-loaded reference data (no
-- checkpoint path) — no engine stop needed; the deploy's restart loads them.
-- Fixed sprite UUIDs so re-runs and the down are deterministic; every insert
-- is ON CONFLICT / NOT EXISTS guarded. The guards establish rows only if
-- absent — a re-run deliberately does NOT repair or validate pre-existing
-- rows, identity fields included, because render_scale and frame_rate are
-- live-tunable through the editor and convergence would revert that tuning.
--
-- THE SHEETS TRAVEL BY SCP, NOT BY DEPLOY (gitignored): six PNGs renamed
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
            'LLM-641: tileset_pack mana-seed-livestock is missing; LLM-579 should have created it';
    END IF;
END $$;

-- The flock. Deterministic UUIDs (LLM-641 namespace, hand-minted).
INSERT INTO public.npc_sprite (id, name, sheet, frame_width, frame_height, pack_id, behaviors, render_scale)
VALUES
    ('641c0001-0000-4000-8000-000000000001', 'Hen (golden)',        '/tilesets/mana-seed/livestock/livestock-chicken-aaa-v00.png', 64, 64, 'mana-seed-livestock', '["grazer", "ambient"]', 1.0),
    ('641c0002-0000-4000-8000-000000000002', 'Hen (rust)',          '/tilesets/mana-seed/livestock/livestock-chicken-aab-v01.png', 64, 64, 'mana-seed-livestock', '["grazer", "ambient"]', 1.0),
    ('641c0003-0000-4000-8000-000000000003', 'Hen (cream)',         '/tilesets/mana-seed/livestock/livestock-chicken-aba-v01.png', 64, 64, 'mana-seed-livestock', '["grazer", "ambient"]', 1.0),
    ('641c0004-0000-4000-8000-000000000004', 'Rooster (golden)',    '/tilesets/mana-seed/livestock/livestock-chicken-baa-v02.png', 64, 64, 'mana-seed-livestock', '["grazer", "ambient"]', 1.0),
    ('641c0005-0000-4000-8000-000000000005', 'Rooster (slate)',     '/tilesets/mana-seed/livestock/livestock-chicken-bab-v02.png', 64, 64, 'mana-seed-livestock', '["grazer", "ambient"]', 1.0),
    ('641c0006-0000-4000-8000-000000000006', 'Rooster (speckled)',  '/tilesets/mana-seed/livestock/livestock-chicken-bbb-v03.png', 64, 64, 'mana-seed-livestock', '["grazer", "ambient"]', 1.0)
ON CONFLICT (id) DO NOTHING;

-- 8 animations per sprite: walk rows 0-3 (6 frames), peck-as-idle rows 4-7
-- (3 frames, slow — it is foraging, not flickering). Scoped to the chicken
-- ids: the pack also holds the ducks and cattle, whose rows this must not
-- touch.
INSERT INTO public.npc_sprite_animation (sprite_id, direction, animation, row_index, frame_count, frame_rate)
SELECT s.id, a.direction, a.animation, a.row_index, a.frame_count, a.frame_rate
FROM public.npc_sprite s
CROSS JOIN (VALUES
    ('south', 'walk', 0, 6, 7.0),
    ('north', 'walk', 1, 6, 7.0),
    ('east',  'walk', 2, 6, 7.0),
    ('west',  'walk', 3, 6, 7.0),
    ('south', 'idle', 4, 3, 3.0),
    ('north', 'idle', 5, 3, 3.0),
    ('east',  'idle', 6, 3, 3.0),
    ('west',  'idle', 7, 3, 3.0)
) AS a(direction, animation, row_index, frame_count, frame_rate)
WHERE s.id IN (
    '641c0001-0000-4000-8000-000000000001', '641c0002-0000-4000-8000-000000000002',
    '641c0003-0000-4000-8000-000000000003', '641c0004-0000-4000-8000-000000000004',
    '641c0005-0000-4000-8000-000000000005', '641c0006-0000-4000-8000-000000000006')
  AND NOT EXISTS (
      SELECT 1 FROM public.npc_sprite_animation existing
      WHERE existing.sprite_id = s.id
        AND existing.direction = a.direction
        AND existing.animation = a.animation
  );

COMMIT;
