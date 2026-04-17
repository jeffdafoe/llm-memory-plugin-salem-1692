-- ZBBS-043: NPC infrastructure — sprite catalog + NPC instances.
--
-- Three tables:
--   npc_sprite            — character sprite sheets (name + sheet path + frame dims + pack)
--   npc_sprite_animation  — direction × animation kind → row/frame metadata
--   npc                   — placed NPC instances (name, sprite, home + current pos, facing, optional LLM link)
--
-- Milestone 1a: static rendering only — we seed one sprite (Mana Seed Woman A v00) and
-- one NPC (Martha, at the crossroads). Animations get populated later once we confirm
-- the sheet's row-to-direction layout visually.

CREATE TABLE npc_sprite (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name VARCHAR(100) NOT NULL,
    sheet VARCHAR(255) NOT NULL,
    frame_width INT NOT NULL DEFAULT 32,
    frame_height INT NOT NULL DEFAULT 32,
    pack_id UUID REFERENCES tileset_pack(id)
);

CREATE TABLE npc_sprite_animation (
    sprite_id UUID NOT NULL REFERENCES npc_sprite(id) ON DELETE CASCADE,
    direction VARCHAR(5) NOT NULL CHECK (direction IN ('north', 'south', 'east', 'west')),
    animation VARCHAR(10) NOT NULL CHECK (animation IN ('idle', 'walk')),
    row_index INT NOT NULL,
    frame_count INT NOT NULL,
    frame_rate DOUBLE PRECISION NOT NULL DEFAULT 6.0,
    PRIMARY KEY (sprite_id, direction, animation)
);

CREATE TABLE npc (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    display_name VARCHAR(100) NOT NULL,
    sprite_id UUID NOT NULL REFERENCES npc_sprite(id),
    home_x DOUBLE PRECISION NOT NULL,
    home_y DOUBLE PRECISION NOT NULL,
    current_x DOUBLE PRECISION NOT NULL,
    current_y DOUBLE PRECISION NOT NULL,
    facing VARCHAR(5) NOT NULL DEFAULT 'south'
        CHECK (facing IN ('north', 'south', 'east', 'west')),
    llm_memory_agent VARCHAR(100) REFERENCES village_agent(llm_memory_agent),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Seed: NPC pack + one sprite + one test NPC at the crossroads (1280, 704).
INSERT INTO tileset_pack (id, name, url) VALUES (
    '11111111-2222-3333-4444-555555555555',
    'Mana Seed NPC Pack #1',
    'https://seliel-the-shaper.itch.io/'
);

INSERT INTO npc_sprite (id, name, sheet, pack_id) VALUES (
    '22222222-3333-4444-5555-666666666666',
    'Woman A (v00)',
    '/tilesets/mana-seed/npc/woman_A_v00.png',
    '11111111-2222-3333-4444-555555555555'
);

INSERT INTO npc (display_name, sprite_id, home_x, home_y, current_x, current_y, facing) VALUES (
    'Martha',
    '22222222-3333-4444-5555-666666666666',
    1280, 704, 1280, 704, 'south'
);
