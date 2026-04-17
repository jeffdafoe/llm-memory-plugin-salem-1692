-- ZBBS-048: Seed two more NPCs tied to existing agents.
--
--   Ezekiel Crane (zbbs-ezekiel-crane, blacksmith)  → Old Man A sprite
--   Josiah Thorne (zbbs-josiah-thorne, merchant)    → Merchant A sprite
--
-- Both sprite sheets follow the same Mana Seed NPC Pack #1 layout as Woman A:
-- 4 cols × 4 populated rows of 32×32 frames. Rows 0/1/2/3 = south/east/north/west
-- (east = row 1 and west = row 3 per ZBBS-045 correction).
--
-- Spawn positions near the well crossroads so Prudence has company to start.

-- Sprites
INSERT INTO npc_sprite (id, name, sheet, pack_id) VALUES
    ('33333333-4444-5555-6666-777777777777',
     'Old Man A (v00)',
     '/tilesets/mana-seed/npc/old_man_A_v00.png',
     'mana-seed-npc-1'),
    ('44444444-5555-6666-7777-888888888888',
     'Merchant A (v00)',
     '/tilesets/mana-seed/npc/merchant_A_v00.png',
     'mana-seed-npc-1');

-- Animations for Old Man A
INSERT INTO npc_sprite_animation (sprite_id, direction, animation, row_index, frame_count, frame_rate) VALUES
    ('33333333-4444-5555-6666-777777777777', 'south', 'walk', 0, 4, 6.0),
    ('33333333-4444-5555-6666-777777777777', 'east',  'walk', 1, 4, 6.0),
    ('33333333-4444-5555-6666-777777777777', 'north', 'walk', 2, 4, 6.0),
    ('33333333-4444-5555-6666-777777777777', 'west',  'walk', 3, 4, 6.0),
    ('33333333-4444-5555-6666-777777777777', 'south', 'idle', 0, 1, 1.0),
    ('33333333-4444-5555-6666-777777777777', 'east',  'idle', 1, 1, 1.0),
    ('33333333-4444-5555-6666-777777777777', 'north', 'idle', 2, 1, 1.0),
    ('33333333-4444-5555-6666-777777777777', 'west',  'idle', 3, 1, 1.0);

-- Animations for Merchant A
INSERT INTO npc_sprite_animation (sprite_id, direction, animation, row_index, frame_count, frame_rate) VALUES
    ('44444444-5555-6666-7777-888888888888', 'south', 'walk', 0, 4, 6.0),
    ('44444444-5555-6666-7777-888888888888', 'east',  'walk', 1, 4, 6.0),
    ('44444444-5555-6666-7777-888888888888', 'north', 'walk', 2, 4, 6.0),
    ('44444444-5555-6666-7777-888888888888', 'west',  'walk', 3, 4, 6.0),
    ('44444444-5555-6666-7777-888888888888', 'south', 'idle', 0, 1, 1.0),
    ('44444444-5555-6666-7777-888888888888', 'east',  'idle', 1, 1, 1.0),
    ('44444444-5555-6666-7777-888888888888', 'north', 'idle', 2, 1, 1.0),
    ('44444444-5555-6666-7777-888888888888', 'west',  'idle', 3, 1, 1.0);

-- NPCs linked to their agents. Spawn offset east and west of the well.
INSERT INTO npc (display_name, sprite_id, home_x, home_y, current_x, current_y, facing, llm_memory_agent) VALUES
    ('Ezekiel Crane',
     '33333333-4444-5555-6666-777777777777',
     1450, 820, 1450, 820, 'south',
     'zbbs-ezekiel-crane'),
    ('Josiah Thorne',
     '44444444-5555-6666-7777-888888888888',
     1166, 820, 1166, 820, 'south',
     'zbbs-josiah-thorne');
