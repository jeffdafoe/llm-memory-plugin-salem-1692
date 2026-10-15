-- LLM-783: visitors on the farmer base, dressed as city folk.
--
-- Ten farmer_base rig sprites (shape: LLM-691), one for each look a visitor
-- can take. The engine picks a visitor's sprite BY NAME (engine/sim/visitor.go
-- visitorSpriteName), so these names must match the three tables there:
-- passerThroughSprite (five archetypes), FactorSpriteName, and
-- merchantBuyerSpriteNames (buyers, shortage peddlers, the carter).
--
-- The city look: colours the wardrobe does not offer villagers (crimson, wine,
-- vivid blue, purple; farmerColours in engine/sim/farmer_wardrobe.go), two
-- pack pieces the wardrobe leaves out (14head cowboyhat as a broad
-- traveller's hat, 07fot2 curlytoeshoes), and costly pieces worn together
-- (cloak and mantle, vest, gloves, cuffed boots). All male bodies: the
-- visitor name pool is male (visitor_persona.go).
--
-- A visitor in the village at deploy keeps his old sprite_id until the
-- engine's start rehydrates him, which re-resolves the sprite by name. The
-- old one-sheet sprites stay in the catalog, so a code revert is the rollback.
--
-- The sheets are already on the VPS (the pack's farmer_base_sheets/ is copied
-- whole, LLM-691). Rerun-safe: a row already at one of these ids is set to
-- the values below, so a stale row cannot survive under the expected name.

BEGIN;

INSERT INTO public.npc_sprite (id, name, sheet, frame_width, frame_height, pack_id, behaviors, render_scale, rig, layers)
VALUES
('783f0c01-0000-4000-8000-000000000001', 'Visitor factor (farmer)',
 '/tilesets/mana-seed/farmer/sheets/01body/fbas_01body_human_00.png', 64, 64, 'mana-seed-farmer', '[]', 2.0, 'farmer_base',
 '[
   {"sheet": "/tilesets/mana-seed/farmer/sheets/01body/fbas_01body_human_00.png", "ramps": {"skin": 2}},
   {"sheet": "/tilesets/mana-seed/farmer/sheets/00undr/fbas_00undr_cloakwithmantleplain_00b.png", "ramps": {"c4": 3}, "behind": true},
   {"sheet": "/tilesets/mana-seed/farmer/sheets/04lwr1/fbas_04lwr1_longpants_00a.png", "ramps": {"c3": 3}},
   {"sheet": "/tilesets/mana-seed/farmer/sheets/05shrt/fbas_05shrt_longshirt_00a.png", "ramps": {"c3": 0}},
   {"sheet": "/tilesets/mana-seed/farmer/sheets/07fot2/fbas_07fot2_cuffedboots_00a.png", "ramps": {"c3": 33}},
   {"sheet": "/tilesets/mana-seed/farmer/sheets/09hand/fbas_09hand_gloves_00a.png", "ramps": {"c3": 3}},
   {"sheet": "/tilesets/mana-seed/farmer/sheets/10outr/fbas_10outr_vest_00a.png", "ramps": {"c3": 41}},
   {"sheet": "/tilesets/mana-seed/farmer/sheets/11neck/fbas_11neck_cloakwithmantleplain_00b.png", "ramps": {"c4": 3}},
   {"sheet": "/tilesets/mana-seed/farmer/sheets/13hair/fbas_13hair_dapper_00.png", "ramps": {"hair": 36}},
   {"sheet": "/tilesets/mana-seed/farmer/sheets/14head/fbas_14head_cowboyhat_00d.png", "ramps": {"c4": 3, "c3": 41}}
 ]'),
('783f0c01-0000-4000-8000-000000000002', 'Visitor trader blue (farmer)',
 '/tilesets/mana-seed/farmer/sheets/01body/fbas_01body_human_00.png', 64, 64, 'mana-seed-farmer', '[]', 2.0, 'farmer_base',
 '[
   {"sheet": "/tilesets/mana-seed/farmer/sheets/01body/fbas_01body_human_00.png", "ramps": {"skin": 1}},
   {"sheet": "/tilesets/mana-seed/farmer/sheets/02sock/fbas_02sock_stockings_00a.png", "ramps": {"c3": 0}},
   {"sheet": "/tilesets/mana-seed/farmer/sheets/04lwr1/fbas_04lwr1_longpants_00a.png", "ramps": {"c3": 1}},
   {"sheet": "/tilesets/mana-seed/farmer/sheets/05shrt/fbas_05shrt_longshirt_00a.png", "ramps": {"c3": 0}},
   {"sheet": "/tilesets/mana-seed/farmer/sheets/07fot2/fbas_07fot2_curlytoeshoes_00a.png", "ramps": {"c3": 3}},
   {"sheet": "/tilesets/mana-seed/farmer/sheets/10outr/fbas_10outr_vest_00a.png", "ramps": {"c3": 13}},
   {"sheet": "/tilesets/mana-seed/farmer/sheets/11neck/fbas_11neck_scarf_00b.png", "ramps": {"c4": 8}},
   {"sheet": "/tilesets/mana-seed/farmer/sheets/13hair/fbas_13hair_longbound_00.png", "ramps": {"hair": 31}},
   {"sheet": "/tilesets/mana-seed/farmer/sheets/14head/fbas_14head_boaterhat_00d.png", "ramps": {"c4": 3, "c3": 13}}
 ]'),
('783f0c01-0000-4000-8000-000000000003', 'Visitor trader green (farmer)',
 '/tilesets/mana-seed/farmer/sheets/01body/fbas_01body_human_00.png', 64, 64, 'mana-seed-farmer', '[]', 2.0, 'farmer_base',
 '[
   {"sheet": "/tilesets/mana-seed/farmer/sheets/01body/fbas_01body_human_00.png", "ramps": {"skin": 7}},
   {"sheet": "/tilesets/mana-seed/farmer/sheets/04lwr1/fbas_04lwr1_longpants_00a.png", "ramps": {"c3": 32}},
   {"sheet": "/tilesets/mana-seed/farmer/sheets/05shrt/fbas_05shrt_longshirt_00a.png", "ramps": {"c3": 0}},
   {"sheet": "/tilesets/mana-seed/farmer/sheets/07fot2/fbas_07fot2_curlytoeshoes_00a.png", "ramps": {"c3": 33}},
   {"sheet": "/tilesets/mana-seed/farmer/sheets/10outr/fbas_10outr_vest_00a.png", "ramps": {"c3": 17}},
   {"sheet": "/tilesets/mana-seed/farmer/sheets/11neck/fbas_11neck_scarf_00b.png", "ramps": {"c4": 12}},
   {"sheet": "/tilesets/mana-seed/farmer/sheets/13hair/fbas_13hair_bushy_00.png", "ramps": {"hair": 29}},
   {"sheet": "/tilesets/mana-seed/farmer/sheets/14head/fbas_14head_floppyhat_00d.png", "ramps": {"c4": 37, "c3": 16}}
 ]'),
('783f0c01-0000-4000-8000-000000000004', 'Visitor trader purple (farmer)',
 '/tilesets/mana-seed/farmer/sheets/01body/fbas_01body_human_00.png', 64, 64, 'mana-seed-farmer', '[]', 2.0, 'farmer_base',
 '[
   {"sheet": "/tilesets/mana-seed/farmer/sheets/01body/fbas_01body_human_00.png", "ramps": {"skin": 6}},
   {"sheet": "/tilesets/mana-seed/farmer/sheets/02sock/fbas_02sock_stockings_00a.png", "ramps": {"c3": 0}},
   {"sheet": "/tilesets/mana-seed/farmer/sheets/04lwr1/fbas_04lwr1_longpants_00a.png", "ramps": {"c3": 3}},
   {"sheet": "/tilesets/mana-seed/farmer/sheets/05shrt/fbas_05shrt_longshirt_00a.png", "ramps": {"c3": 0}},
   {"sheet": "/tilesets/mana-seed/farmer/sheets/07fot2/fbas_07fot2_curlytoeshoes_00a.png", "ramps": {"c3": 44}},
   {"sheet": "/tilesets/mana-seed/farmer/sheets/10outr/fbas_10outr_vest_00a.png", "ramps": {"c3": 44}},
   {"sheet": "/tilesets/mana-seed/farmer/sheets/11neck/fbas_11neck_scarf_00b.png", "ramps": {"c4": 50}},
   {"sheet": "/tilesets/mana-seed/farmer/sheets/13hair/fbas_13hair_flattop_00.png", "ramps": {"hair": 34}},
   {"sheet": "/tilesets/mana-seed/farmer/sheets/14head/fbas_14head_boaterhat_00d.png", "ramps": {"c4": 2, "c3": 44}}
 ]'),
('783f0c01-0000-4000-8000-000000000005', 'Visitor trader orange (farmer)',
 '/tilesets/mana-seed/farmer/sheets/01body/fbas_01body_human_00.png', 64, 64, 'mana-seed-farmer', '[]', 2.0, 'farmer_base',
 '[
   {"sheet": "/tilesets/mana-seed/farmer/sheets/01body/fbas_01body_human_00.png", "ramps": {"skin": 0}},
   {"sheet": "/tilesets/mana-seed/farmer/sheets/03fot1/fbas_03fot1_boots_00a.png", "ramps": {"c3": 33}},
   {"sheet": "/tilesets/mana-seed/farmer/sheets/04lwr1/fbas_04lwr1_longpants_00a.png", "ramps": {"c3": 31}},
   {"sheet": "/tilesets/mana-seed/farmer/sheets/05shrt/fbas_05shrt_longshirt_00a.png", "ramps": {"c3": 0}},
   {"sheet": "/tilesets/mana-seed/farmer/sheets/09hand/fbas_09hand_gloves_00a.png", "ramps": {"c3": 31}},
   {"sheet": "/tilesets/mana-seed/farmer/sheets/10outr/fbas_10outr_vest_00a.png", "ramps": {"c3": 37}},
   {"sheet": "/tilesets/mana-seed/farmer/sheets/11neck/fbas_11neck_scarf_00b.png", "ramps": {"c4": 41}},
   {"sheet": "/tilesets/mana-seed/farmer/sheets/13hair/fbas_13hair_dapper_00.png", "ramps": {"hair": 39}},
   {"sheet": "/tilesets/mana-seed/farmer/sheets/14head/fbas_14head_cowboyhat_00d.png", "ramps": {"c4": 37, "c3": 36}}
 ]'),
('783f0c01-0000-4000-8000-000000000006', 'Visitor messenger (farmer)',
 '/tilesets/mana-seed/farmer/sheets/01body/fbas_01body_human_00.png', 64, 64, 'mana-seed-farmer', '[]', 2.0, 'farmer_base',
 '[
   {"sheet": "/tilesets/mana-seed/farmer/sheets/01body/fbas_01body_human_00.png", "ramps": {"skin": 3}},
   {"sheet": "/tilesets/mana-seed/farmer/sheets/00undr/fbas_00undr_cloakplain_00d.png", "ramps": {"c4": 22, "c3": 3}, "behind": true},
   {"sheet": "/tilesets/mana-seed/farmer/sheets/03fot1/fbas_03fot1_boots_00a.png", "ramps": {"c3": 33}},
   {"sheet": "/tilesets/mana-seed/farmer/sheets/04lwr1/fbas_04lwr1_longpants_00a.png", "ramps": {"c3": 20}},
   {"sheet": "/tilesets/mana-seed/farmer/sheets/05shrt/fbas_05shrt_longshirt_00a.png", "ramps": {"c3": 0}},
   {"sheet": "/tilesets/mana-seed/farmer/sheets/09hand/fbas_09hand_gloves_00a.png", "ramps": {"c3": 32}},
   {"sheet": "/tilesets/mana-seed/farmer/sheets/11neck/fbas_11neck_cloakplain_00d.png", "ramps": {"c4": 22, "c3": 3}},
   {"sheet": "/tilesets/mana-seed/farmer/sheets/13hair/fbas_13hair_flattop_00.png", "ramps": {"hair": 37}},
   {"sheet": "/tilesets/mana-seed/farmer/sheets/14head/fbas_14head_cowboyhat_00d.png", "ramps": {"c4": 37, "c3": 33}}
 ]'),
('783f0c01-0000-4000-8000-000000000007', 'Visitor musician (farmer)',
 '/tilesets/mana-seed/farmer/sheets/01body/fbas_01body_human_00.png', 64, 64, 'mana-seed-farmer', '[]', 2.0, 'farmer_base',
 '[
   {"sheet": "/tilesets/mana-seed/farmer/sheets/01body/fbas_01body_human_00.png", "ramps": {"skin": 8}},
   {"sheet": "/tilesets/mana-seed/farmer/sheets/02sock/fbas_02sock_sockshigh_00a.png", "ramps": {"c3": 0}},
   {"sheet": "/tilesets/mana-seed/farmer/sheets/04lwr1/fbas_04lwr1_longpants_00a.png", "ramps": {"c3": 13}},
   {"sheet": "/tilesets/mana-seed/farmer/sheets/05shrt/fbas_05shrt_longshirt_00a.png", "ramps": {"c3": 0}},
   {"sheet": "/tilesets/mana-seed/farmer/sheets/07fot2/fbas_07fot2_curlytoeshoes_00a.png", "ramps": {"c3": 41}},
   {"sheet": "/tilesets/mana-seed/farmer/sheets/10outr/fbas_10outr_vest_00a.png", "ramps": {"c3": 36}},
   {"sheet": "/tilesets/mana-seed/farmer/sheets/11neck/fbas_11neck_scarf_00b.png", "ramps": {"c4": 41}},
   {"sheet": "/tilesets/mana-seed/farmer/sheets/13hair/fbas_13hair_longwavy_00.png", "ramps": {"hair": 34}},
   {"sheet": "/tilesets/mana-seed/farmer/sheets/14head/fbas_14head_floppyhat_00d.png", "ramps": {"c4": 8, "c3": 36}}
 ]'),
('783f0c01-0000-4000-8000-000000000008', 'Visitor preacher (farmer)',
 '/tilesets/mana-seed/farmer/sheets/01body/fbas_01body_human_00.png', 64, 64, 'mana-seed-farmer', '[]', 2.0, 'farmer_base',
 '[
   {"sheet": "/tilesets/mana-seed/farmer/sheets/01body/fbas_01body_human_00.png", "ramps": {"skin": 0}},
   {"sheet": "/tilesets/mana-seed/farmer/sheets/02sock/fbas_02sock_stockings_00a.png", "ramps": {"c3": 3}},
   {"sheet": "/tilesets/mana-seed/farmer/sheets/03fot1/fbas_03fot1_shoes_00a.png", "ramps": {"c3": 3}},
   {"sheet": "/tilesets/mana-seed/farmer/sheets/04lwr1/fbas_04lwr1_longpants_00a.png", "ramps": {"c3": 3}},
   {"sheet": "/tilesets/mana-seed/farmer/sheets/05shrt/fbas_05shrt_longshirt_00a.png", "ramps": {"c3": 0}},
   {"sheet": "/tilesets/mana-seed/farmer/sheets/11neck/fbas_11neck_mantleplain_00b.png", "ramps": {"c4": 3}},
   {"sheet": "/tilesets/mana-seed/farmer/sheets/13hair/fbas_13hair_bushy_00.png", "ramps": {"hair": 0}},
   {"sheet": "/tilesets/mana-seed/farmer/sheets/14head/fbas_14head_cowboyhat_00d.png", "ramps": {"c4": 3, "c3": 3}}
 ]'),
('783f0c01-0000-4000-8000-000000000009', 'Visitor scholar (farmer)',
 '/tilesets/mana-seed/farmer/sheets/01body/fbas_01body_human_00.png', 64, 64, 'mana-seed-farmer', '[]', 2.0, 'farmer_base',
 '[
   {"sheet": "/tilesets/mana-seed/farmer/sheets/01body/fbas_01body_human_00.png", "ramps": {"skin": 4}},
   {"sheet": "/tilesets/mana-seed/farmer/sheets/02sock/fbas_02sock_stockings_00a.png", "ramps": {"c3": 0}},
   {"sheet": "/tilesets/mana-seed/farmer/sheets/04lwr1/fbas_04lwr1_longpants_00a.png", "ramps": {"c3": 1}},
   {"sheet": "/tilesets/mana-seed/farmer/sheets/05shrt/fbas_05shrt_longshirt_00a.png", "ramps": {"c3": 0}},
   {"sheet": "/tilesets/mana-seed/farmer/sheets/07fot2/fbas_07fot2_curlytoeshoes_00a.png", "ramps": {"c3": 3}},
   {"sheet": "/tilesets/mana-seed/farmer/sheets/10outr/fbas_10outr_vest_00a.png", "ramps": {"c3": 42}},
   {"sheet": "/tilesets/mana-seed/farmer/sheets/11neck/fbas_11neck_mantleplain_00b.png", "ramps": {"c4": 8}},
   {"sheet": "/tilesets/mana-seed/farmer/sheets/12face/fbas_12face_glasses_00a.png", "ramps": {"c3": 3}},
   {"sheet": "/tilesets/mana-seed/farmer/sheets/13hair/fbas_13hair_dapper_00.png", "ramps": {"hair": 2}}
 ]'),
('783f0c01-0000-4000-8000-000000000010', 'Visitor surgeon (farmer)',
 '/tilesets/mana-seed/farmer/sheets/01body/fbas_01body_human_00.png', 64, 64, 'mana-seed-farmer', '[]', 2.0, 'farmer_base',
 '[
   {"sheet": "/tilesets/mana-seed/farmer/sheets/01body/fbas_01body_human_00.png", "ramps": {"skin": 5}},
   {"sheet": "/tilesets/mana-seed/farmer/sheets/00undr/fbas_00undr_cloakplain_00d.png", "ramps": {"c4": 2, "c3": 42}, "behind": true},
   {"sheet": "/tilesets/mana-seed/farmer/sheets/04lwr1/fbas_04lwr1_longpants_00a.png", "ramps": {"c3": 3}},
   {"sheet": "/tilesets/mana-seed/farmer/sheets/05shrt/fbas_05shrt_longshirt_00a.png", "ramps": {"c3": 0}},
   {"sheet": "/tilesets/mana-seed/farmer/sheets/07fot2/fbas_07fot2_cuffedboots_00a.png", "ramps": {"c3": 33}},
   {"sheet": "/tilesets/mana-seed/farmer/sheets/09hand/fbas_09hand_gloves_00a.png", "ramps": {"c3": 3}},
   {"sheet": "/tilesets/mana-seed/farmer/sheets/10outr/fbas_10outr_vest_00a.png", "ramps": {"c3": 3}},
   {"sheet": "/tilesets/mana-seed/farmer/sheets/11neck/fbas_11neck_cloakplain_00d.png", "ramps": {"c4": 2, "c3": 42}},
   {"sheet": "/tilesets/mana-seed/farmer/sheets/13hair/fbas_13hair_flattop_00.png", "ramps": {"hair": 3}},
   {"sheet": "/tilesets/mana-seed/farmer/sheets/14head/fbas_14head_floppyhat_00d.png", "ramps": {"c4": 2, "c3": 3}}
 ]')
ON CONFLICT (id) DO UPDATE SET
    name = EXCLUDED.name, sheet = EXCLUDED.sheet,
    frame_width = EXCLUDED.frame_width, frame_height = EXCLUDED.frame_height,
    pack_id = EXCLUDED.pack_id, behaviors = EXCLUDED.behaviors,
    render_scale = EXCLUDED.render_scale, rig = EXCLUDED.rig, layers = EXCLUDED.layers;

COMMIT;
