-- ZBBS-055: drop Baby A/B and Merchant D sprites — Jeff confirmed they
-- won't be used. Animation rows CASCADE via the FK on sprite_id.
-- Safe to run only when no placed NPC references these sprites.

DELETE FROM npc_sprite WHERE id IN (
    '953a741d-dec4-4743-9728-79146a132f71',
    'a737b21e-0350-43d4-91db-7c1e86eda860',
    '614d1226-2949-42f6-996e-874e286385aa',
    '26bdbc83-f81d-4c23-8877-f97e7e08a6d4',
    'eb414a51-96ee-44c3-8489-bcfd65194be4',
    '742b4f59-a24a-4c05-8828-3125fc1218d0',
    '393fd713-c98e-4fd9-912c-68f5b8edab9b',
    'a0a5bc36-d3c4-41a8-b01c-2c4d5f96b2e4',
    'd28362c8-0128-469e-bf3e-1e1f528c6669',
    '483f348e-62f8-463f-8086-0b50f1674800',
    'faa9f77c-833a-476e-8093-f38a128618fa',
    '30922691-9333-4ea5-a670-7318bdfb5fe8',
    '2a396b9f-ab26-45b7-876c-865a08dc3b9d',
    '7e72350f-f61c-430d-bcc1-41310094ffb7'
);
