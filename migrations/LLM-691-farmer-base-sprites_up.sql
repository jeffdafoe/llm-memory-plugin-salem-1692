-- LLM-691: paper-doll character sprites on the Mana Seed Farmer Sprite System,
-- and Abraham Warren as the first villager dressed in one.
--
-- (1) TWO COLUMNS ON npc_sprite.
--     rig    — names an animation table the CLIENT owns (client/scripts/
--              farmer_rig.gd). 'farmer_base' is the only rig. NULL for every
--              classic one-sheet sprite, which keeps animating from its
--              npc_sprite_animation rows.
--     layers — the rig sprite's outfit: a JSON array of
--              {"sheet": <url path>, "ramps": {<slot>: <index>}, "behind"?: bool},
--              body first, then bottom to top. The client stacks the sheets
--              (all share one 64x64 cell layout) and recolours each through
--              its palette-swap shader; slot names and ramp indexes are
--              client/scripts/farmer_palettes.gd. The engine never reads it.
--     A rig sprite has no npc_sprite_animation rows. Its sheet column holds
--     the body sheet (NOT NULL, and what a one-sheet consumer would show);
--     frame_width / frame_height are the 64px cell.
--
-- (2) ABRAHAM WARREN, a hand, re-dressed for 1692: linen shirt, green wool
--     vest, brown breeches, boots, dark hair under a brown hat. He shared the
--     "Man B (v03)" sheet with Jeff's PC; that sprite row is untouched. A hand
--     clears roads and mends at the town's charge, so he is the first to swing
--     the hatchet the client plays while a repair runs. actor is engine-owned,
--     but deploy.sh runs stop -> migrate -> start behind a confirmed
--     checkpoint, so nothing overwrites the switch; on a fresh replay there is
--     no Abraham and it matches zero rows.
--
-- THE SHEETS TRAVEL BY SCP, NOT BY DEPLOY: the pack's farmer_base_sheets/ to
-- /var/www/llm-memory-salem-1692/tilesets/mana-seed/farmer/sheets/ (same
-- sub-folders and names), and farmer_base_effects/"farmer tool 001 v00.png"
-- to .../farmer/effects/farmer_tool_001_v00.png (owner www-data). A missing
-- layer sheet drops that layer; a missing body leaves Abraham undrawn.
--
-- Rerun-safe: ADD COLUMN IF NOT EXISTS, constraints dropped and re-added,
-- inserts ON CONFLICT guarded, and the switch only moves Abraham off his old
-- sprite.

BEGIN;

ALTER TABLE public.npc_sprite ADD COLUMN IF NOT EXISTS rig varchar(32);
ALTER TABLE public.npc_sprite ADD COLUMN IF NOT EXISTS layers jsonb NOT NULL DEFAULT '[]';

ALTER TABLE public.npc_sprite DROP CONSTRAINT IF EXISTS npc_sprite_rig_check;
ALTER TABLE public.npc_sprite ADD CONSTRAINT npc_sprite_rig_check
    CHECK (rig IS NULL OR rig IN ('farmer_base'));

ALTER TABLE public.npc_sprite DROP CONSTRAINT IF EXISTS npc_sprite_layers_check;
ALTER TABLE public.npc_sprite ADD CONSTRAINT npc_sprite_layers_check
    CHECK (jsonb_typeof(layers) = 'array');

INSERT INTO public.tileset_pack (id, name, url)
VALUES ('mana-seed-farmer', 'Farmer Sprite System', 'https://seliel-the-shaper.itch.io/mana-seed')
ON CONFLICT (id) DO NOTHING;

INSERT INTO public.npc_sprite (id, name, sheet, frame_width, frame_height, pack_id, behaviors, render_scale, rig, layers)
VALUES ('691f0c01-0000-4000-8000-000000000001', 'Abraham Warren (farmer)',
        '/tilesets/mana-seed/farmer/sheets/01body/fbas_01body_human_00.png',
        64, 64, 'mana-seed-farmer', '[]', 2.0, 'farmer_base',
        '[
          {"sheet": "/tilesets/mana-seed/farmer/sheets/01body/fbas_01body_human_00.png", "ramps": {"skin": 0}},
          {"sheet": "/tilesets/mana-seed/farmer/sheets/03fot1/fbas_03fot1_boots_00a.png", "ramps": {"c3": 33}},
          {"sheet": "/tilesets/mana-seed/farmer/sheets/04lwr1/fbas_04lwr1_longpants_00a.png", "ramps": {"c3": 32}},
          {"sheet": "/tilesets/mana-seed/farmer/sheets/05shrt/fbas_05shrt_longshirt_00a.png", "ramps": {"c3": 0}},
          {"sheet": "/tilesets/mana-seed/farmer/sheets/10outr/fbas_10outr_vest_00a.png", "ramps": {"c3": 19}},
          {"sheet": "/tilesets/mana-seed/farmer/sheets/13hair/fbas_13hair_dapper_00.png", "ramps": {"hair": 37}},
          {"sheet": "/tilesets/mana-seed/farmer/sheets/14head/fbas_14head_boaterhat_00d.png", "ramps": {"c4": 37, "c3": 3}}
        ]')
ON CONFLICT (id) DO NOTHING;

UPDATE public.actor
   SET sprite_id = '691f0c01-0000-4000-8000-000000000001'
 WHERE id = '019da6b5-3143-71e0-9f47-6bf3af456524'
   AND sprite_id = 'fd124ed1-b225-466c-a3ee-455f7a25c59d';

COMMIT;
