-- ZBBS-023: Break up the nature dumping ground into specific categories
-- Moves trees, bushes, stumps, rocks, and water stuff out of nature.
-- Leaves mushrooms, grass, and misc debris in nature.

-- Trees (Tiny Swords + Mana Seed fallen trees)
UPDATE asset SET category = 'tree'
WHERE name IN ('Tree 1', 'Tree 2', 'Tree 3', 'Tree 4',
               'Fallen Tree', 'Fallen Tree (Large)');

-- Bushes (Mana Seed)
UPDATE asset SET category = 'bush'
WHERE name IN ('Berry Bush', 'Berry Bush Cluster', 'Blueberry Bush', 'Bush');

-- Stumps (Mana Seed) — consolidate with the existing stump category
UPDATE asset SET category = 'stump'
WHERE name IN ('Big Stump', 'Small Stump', 'Stump (Axe)', 'Tree Stump');

-- Rocks (new category) — Mana Seed rocks + Tiny Swords Rock 1-4
UPDATE asset SET category = 'rock'
WHERE name IN ('Boulder Pair', 'Flat Rock', 'Large Rocks', 'Medium Rocks',
               'Pebble', 'River Rock', 'Small Rock', 'Tiny Rock',
               'Rock 1', 'Rock 2', 'Rock 3', 'Rock 4');

-- Water features (new category) — lily pads, water plants, water rocks
UPDATE asset SET category = 'water-features'
WHERE name IN ('Cattails', 'Light Lily Pad', 'Lily Pad', 'Small Lily Pad', 'Tiny Lily Pad',
               'Water Plant', 'Water Vine',
               'Water Rock 1', 'Water Rock 2', 'Water Rock 3', 'Water Rock 4');
