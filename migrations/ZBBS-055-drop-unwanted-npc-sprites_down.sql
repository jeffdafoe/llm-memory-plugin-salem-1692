-- Reversal of ZBBS-055 — re-insert the Baby A/B and Merchant D sprites.
-- Kept in sync with ZBBS-054 for the data, so this mostly duplicates the
-- relevant subset of the up migration there.

INSERT INTO npc_sprite (id, name, sheet, frame_width, frame_height, pack_id) VALUES
    ('953a741d-dec4-4743-9728-79146a132f71', 'Baby A (v00)', '/tilesets/mana-seed/npc/baby_A_v00.png', 32, 32, 'mana-seed-npc-1'),
    ('a737b21e-0350-43d4-91db-7c1e86eda860', 'Baby A (v01)', '/tilesets/mana-seed/npc/baby_A_v01.png', 32, 32, 'mana-seed-npc-1'),
    ('614d1226-2949-42f6-996e-874e286385aa', 'Baby A (v02)', '/tilesets/mana-seed/npc/baby_A_v02.png', 32, 32, 'mana-seed-npc-1'),
    ('26bdbc83-f81d-4c23-8877-f97e7e08a6d4', 'Baby A (v03)', '/tilesets/mana-seed/npc/baby_A_v03.png', 32, 32, 'mana-seed-npc-1'),
    ('eb414a51-96ee-44c3-8489-bcfd65194be4', 'Baby A (v04)', '/tilesets/mana-seed/npc/baby_A_v04.png', 32, 32, 'mana-seed-npc-1'),
    ('742b4f59-a24a-4c05-8828-3125fc1218d0', 'Baby B (v00)', '/tilesets/mana-seed/npc/baby_B_v00.png', 32, 32, 'mana-seed-npc-1'),
    ('393fd713-c98e-4fd9-912c-68f5b8edab9b', 'Baby B (v01)', '/tilesets/mana-seed/npc/baby_B_v01.png', 32, 32, 'mana-seed-npc-1'),
    ('a0a5bc36-d3c4-41a8-b01c-2c4d5f96b2e4', 'Baby B (v02)', '/tilesets/mana-seed/npc/baby_B_v02.png', 32, 32, 'mana-seed-npc-1'),
    ('d28362c8-0128-469e-bf3e-1e1f528c6669', 'Baby B (v03)', '/tilesets/mana-seed/npc/baby_B_v03.png', 32, 32, 'mana-seed-npc-1'),
    ('483f348e-62f8-463f-8086-0b50f1674800', 'Baby B (v04)', '/tilesets/mana-seed/npc/baby_B_v04.png', 32, 32, 'mana-seed-npc-1'),
    ('faa9f77c-833a-476e-8093-f38a128618fa', 'Merchant D (v00)', '/tilesets/mana-seed/npc/merchant_D_v00.png', 32, 32, 'mana-seed-npc-1'),
    ('30922691-9333-4ea5-a670-7318bdfb5fe8', 'Merchant D (v01)', '/tilesets/mana-seed/npc/merchant_D_v01.png', 32, 32, 'mana-seed-npc-1'),
    ('2a396b9f-ab26-45b7-876c-865a08dc3b9d', 'Merchant D (v02)', '/tilesets/mana-seed/npc/merchant_D_v02.png', 32, 32, 'mana-seed-npc-1'),
    ('7e72350f-f61c-430d-bcc1-41310094ffb7', 'Merchant D (v03)', '/tilesets/mana-seed/npc/merchant_D_v03.png', 32, 32, 'mana-seed-npc-1');

INSERT INTO npc_sprite_animation (sprite_id, direction, animation, row_index, frame_count, frame_rate)
SELECT id, dir, anim, row, fc, fr FROM (VALUES
    ('953a741d-dec4-4743-9728-79146a132f71'::uuid),
    ('a737b21e-0350-43d4-91db-7c1e86eda860'::uuid),
    ('614d1226-2949-42f6-996e-874e286385aa'::uuid),
    ('26bdbc83-f81d-4c23-8877-f97e7e08a6d4'::uuid),
    ('eb414a51-96ee-44c3-8489-bcfd65194be4'::uuid),
    ('742b4f59-a24a-4c05-8828-3125fc1218d0'::uuid),
    ('393fd713-c98e-4fd9-912c-68f5b8edab9b'::uuid),
    ('a0a5bc36-d3c4-41a8-b01c-2c4d5f96b2e4'::uuid),
    ('d28362c8-0128-469e-bf3e-1e1f528c6669'::uuid),
    ('483f348e-62f8-463f-8086-0b50f1674800'::uuid),
    ('faa9f77c-833a-476e-8093-f38a128618fa'::uuid),
    ('30922691-9333-4ea5-a670-7318bdfb5fe8'::uuid),
    ('2a396b9f-ab26-45b7-876c-865a08dc3b9d'::uuid),
    ('7e72350f-f61c-430d-bcc1-41310094ffb7'::uuid)
) AS s(id)
CROSS JOIN (VALUES
    ('south', 'idle', 0, 1, 1),
    ('south', 'walk', 0, 4, 6),
    ('east',  'idle', 1, 1, 1),
    ('east',  'walk', 1, 4, 6),
    ('north', 'idle', 2, 1, 1),
    ('north', 'walk', 2, 4, 6),
    ('west',  'idle', 3, 1, 1),
    ('west',  'walk', 3, 4, 6)
) AS a(dir, anim, row, fc, fr);
