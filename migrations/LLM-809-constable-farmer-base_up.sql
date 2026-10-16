-- LLM-809: Constable Gideon Marsh on the farmer base (rig shape: LLM-691).
--
-- The look: white linen shirt, charcoal vest and breeches, cuffed boots, a
-- dark brown cloak with mantle and a dark brown broad hat (14head cowboyhat,
-- as the LLM-783 visitors wear it) with a charcoal band. Layers in the order
-- FarmerOutfit composes them: body, the cloak's back half behind, then the
-- front layers in pack order.
--
-- The switch only moves him off "Guard (v00)", so a rerun or a later re-dress
-- is left alone. Gideon is a live-created NPC (LLM-514): on a fresh replay he
-- does not exist and the UPDATE matches zero rows. actor is engine-owned, but
-- deploy.sh runs stop -> migrate -> start, so the checkpoint cannot overwrite
-- the switch. The sheets are already on the VPS (LLM-691 copied the pack's
-- farmer_base_sheets/ whole).
--
-- Rerun-safe: a row already at the sprite id is set to the values below.

BEGIN;

INSERT INTO public.npc_sprite (id, name, sheet, frame_width, frame_height, pack_id, behaviors, render_scale, anchor_y, rig, layers)
VALUES ('809f0c01-0000-4000-8000-000000000001', 'Constable Gideon Marsh (farmer)',
        '/tilesets/mana-seed/farmer/sheets/01body/fbas_01body_human_00.png', 64, 64, 'mana-seed-farmer', '[]', 2.0, 0.9, 'farmer_base',
        '[
          {"sheet": "/tilesets/mana-seed/farmer/sheets/01body/fbas_01body_human_00.png", "ramps": {"skin": 0}},
          {"sheet": "/tilesets/mana-seed/farmer/sheets/00undr/fbas_00undr_cloakwithmantleplain_00b.png", "ramps": {"c4": 37}, "behind": true},
          {"sheet": "/tilesets/mana-seed/farmer/sheets/04lwr1/fbas_04lwr1_longpants_00a.png", "ramps": {"c3": 3}},
          {"sheet": "/tilesets/mana-seed/farmer/sheets/05shrt/fbas_05shrt_longshirt_00a.png", "ramps": {"c3": 0}},
          {"sheet": "/tilesets/mana-seed/farmer/sheets/07fot2/fbas_07fot2_cuffedboots_00a.png", "ramps": {"c3": 33}},
          {"sheet": "/tilesets/mana-seed/farmer/sheets/10outr/fbas_10outr_vest_00a.png", "ramps": {"c3": 3}},
          {"sheet": "/tilesets/mana-seed/farmer/sheets/11neck/fbas_11neck_cloakwithmantleplain_00b.png", "ramps": {"c4": 37}},
          {"sheet": "/tilesets/mana-seed/farmer/sheets/13hair/fbas_13hair_dapper_00.png", "ramps": {"hair": 3}},
          {"sheet": "/tilesets/mana-seed/farmer/sheets/14head/fbas_14head_cowboyhat_00d.png", "ramps": {"c4": 37, "c3": 3}}
        ]')
ON CONFLICT (id) DO UPDATE SET
    name = EXCLUDED.name, sheet = EXCLUDED.sheet,
    frame_width = EXCLUDED.frame_width, frame_height = EXCLUDED.frame_height,
    pack_id = EXCLUDED.pack_id, behaviors = EXCLUDED.behaviors,
    render_scale = EXCLUDED.render_scale, anchor_y = EXCLUDED.anchor_y,
    rig = EXCLUDED.rig, layers = EXCLUDED.layers;

UPDATE public.actor
   SET sprite_id = '809f0c01-0000-4000-8000-000000000001'
 WHERE id = '4561da54-eb08-46c8-8f05-ddc0aadaebff'
   AND sprite_id = '4a3286a7-d34d-4a4d-8041-8adc04193148';

COMMIT;
