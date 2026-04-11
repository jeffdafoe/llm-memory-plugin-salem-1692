-- ZBBS-009: Tileset packs — track where assets came from, relocate sheet paths

-- Tileset pack — a purchased or free asset pack (e.g. from itch.io)
CREATE TABLE tileset_pack (
    id VARCHAR(60) NOT NULL,
    name VARCHAR(100) NOT NULL,
    url VARCHAR(500),
    created_at TIMESTAMP(0) WITHOUT TIME ZONE NOT NULL DEFAULT NOW(),
    PRIMARY KEY (id)
);

-- Seed known packs
INSERT INTO tileset_pack (id, name, url) VALUES
    ('mana-seed', 'Mana Seed', 'https://seliel-the-shaper.itch.io/mana-seed'),
    ('seliel-village', 'Pixelart Village Top-Down', 'https://seliel-the-shaper.itch.io/rpg-starter-pack'),
    ('mystic-woods', 'Mystic Woods', 'https://game-endeavor.itch.io/mystic-woods'),
    ('rgs-cc0', 'CC0 Top Down Tileset', 'https://rgs-dev.itch.io/cc0-top-down-tileset-template');

-- Add pack reference to assets
ALTER TABLE asset ADD COLUMN pack_id VARCHAR(60) REFERENCES tileset_pack(id);

-- Tag existing assets with their packs
UPDATE asset SET pack_id = 'mana-seed' WHERE id IN (
    'tree-maple', 'tree-chestnut', 'tree-birch',
    'bush', 'bush-berries', 'rock-small', 'rock-water', 'stump', 'log-pile', 'bush-small',
    'stump-big', 'fallen-log',
    'barrel', 'barrel-open', 'wood-pile', 'wood-shelter', 'crate', 'millstone',
    'well-empty', 'well-bucket', 'well-roof', 'well-wishing', 'shop-front',
    'stall-wood', 'stall-tiled', 'stall-fancy',
    'wagon', 'wagon-covered',
    'lamppost', 'lamppost-sign', 'lamppost-banner'
);

-- Bridge is from the mana-seed summer forest extras
UPDATE asset SET pack_id = 'mana-seed' WHERE id = 'bridge';

-- Relocate all sheet paths: /assets/tilesets/ → /tilesets/
UPDATE asset_state SET sheet = REPLACE(sheet, '/assets/tilesets/', '/tilesets/');
