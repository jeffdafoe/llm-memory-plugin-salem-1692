-- ZBBS-040: Daily rotation + per-asset transition spread.
--
-- Adds midnight rotation for assets with 'rotatable'-tagged states (notice
-- boards, laundry). Each asset declares how its rotatable states cycle via
-- rotation_algo, and how far in time to spread instance flips via
-- transition_spread_seconds.
--
-- transition_spread_seconds also applies to the day/night phase transition
-- from ZBBS-038. Seeded non-zero for every lit/unlit asset so lamps come on
-- gradually at dusk (lamplighter walking the route) rather than all at once.

ALTER TABLE asset
    ADD COLUMN rotation_algo VARCHAR(32) NOT NULL DEFAULT 'random_per_object'
        CHECK (rotation_algo IN ('random_per_object', 'random_per_asset', 'deterministic'));

ALTER TABLE asset
    ADD COLUMN transition_spread_seconds INT NOT NULL DEFAULT 0
        CHECK (transition_spread_seconds >= 0);

ALTER TABLE world_phase
    ADD COLUMN last_rotation_at TIMESTAMPTZ NOT NULL DEFAULT NOW();

INSERT INTO setting (key, value, description, is_public) VALUES
    ('world_rotation_time', '00:00',
     'Time of day (HH:MM in world_timezone) when rotatable assets rotate (notice boards, laundry daily reshuffle)',
     false);

-- Seed spread values per asset. These are first-cut; tune via UPDATE later
-- after seeing them in-game.

-- Laundry: 30 min — villagers hang clothes through the morning.
UPDATE asset SET transition_spread_seconds = 1800 WHERE name LIKE 'Laundry%';

-- Notice boards: 5 min — postings shuffle in a visible ripple.
UPDATE asset SET transition_spread_seconds = 300 WHERE name = 'Notice Board';

-- Lamps: 2 min — lamplighter walks the route at dusk.
UPDATE asset SET transition_spread_seconds = 120
    WHERE name IN ('Lamp Post', 'Lamp Post 2', 'Lamp Post 3', 'Hanging Lantern');

-- Torches, campfires: 1 min — one watchman tends them quickly.
UPDATE asset SET transition_spread_seconds = 60
    WHERE name IN ('Torch', 'Campfire');
