-- ZBBS-023 down: Move everything back to the nature bucket
-- Note: pre-existing bush/tree/stump items (Bush 1-4, Birch/Chestnut/Maple, Tiny Swords stumps)
-- stay where they are. Only items that this migration moved revert.

UPDATE asset SET category = 'nature'
WHERE name IN (
    -- trees moved out
    'Tree 1', 'Tree 2', 'Tree 3', 'Tree 4', 'Fallen Tree', 'Fallen Tree (Large)',
    -- bushes moved out
    'Berry Bush', 'Berry Bush Cluster', 'Blueberry Bush', 'Bush',
    -- stumps moved out
    'Big Stump', 'Small Stump', 'Stump (Axe)', 'Tree Stump',
    -- rocks moved out
    'Boulder Pair', 'Flat Rock', 'Large Rocks', 'Medium Rocks',
    'Pebble', 'River Rock', 'Small Rock', 'Tiny Rock',
    'Rock 1', 'Rock 2', 'Rock 3', 'Rock 4',
    -- water features moved out
    'Cattails', 'Light Lily Pad', 'Lily Pad', 'Small Lily Pad', 'Tiny Lily Pad',
    'Water Plant', 'Water Vine',
    'Water Rock 1', 'Water Rock 2', 'Water Rock 3', 'Water Rock 4'
);
