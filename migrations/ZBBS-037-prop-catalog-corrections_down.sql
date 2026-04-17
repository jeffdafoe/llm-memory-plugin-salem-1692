-- ZBBS-037 down: reverses only the renames + category moves (deletions are not
-- reversible without re-guessing anchors/UUIDs).

-- Undo cascade renames — use temp names again to avoid collisions.
UPDATE asset SET name = '__rev_fp_mixed' WHERE name = 'Flower Pot (Mixed)' AND pack_id = 'mana-seed';
UPDATE asset SET name = '__rev_fp_red'   WHERE name = 'Flower Pot (Red)'   AND pack_id = 'mana-seed';
UPDATE asset SET name = '__rev_fp_viol'  WHERE name = 'Flower Pot (Violet)' AND pack_id = 'mana-seed';
UPDATE asset SET name = '__rev_fp_empty' WHERE name = 'Flower Pot (Empty)' AND pack_id = 'mana-seed';

UPDATE asset SET name = 'Small Barrel'        WHERE name = '__rev_fp_mixed';
UPDATE asset SET name = 'Flower Pot (Mixed)'  WHERE name = '__rev_fp_red';
UPDATE asset SET name = 'Flower Pot (Red)'    WHERE name = '__rev_fp_viol';
UPDATE asset SET name = 'Flower Pot (Violet)' WHERE name = '__rev_fp_empty';

UPDATE asset SET name = 'Crystal Ball' WHERE name = 'Small Pot (Empty)' AND pack_id = 'mana-seed';
UPDATE asset SET name = 'Small Pot'    WHERE name = 'Small Pot (Full)'  AND pack_id = 'mana-seed';

-- Undo simple renames
UPDATE asset SET name = 'Blue Pot'            WHERE name = 'Bucket (Empty)'    AND pack_id = 'mana-seed';
UPDATE asset SET name = 'Scroll'              WHERE name = 'Wood Pile (Small)' AND pack_id = 'mana-seed';
UPDATE asset SET name = 'Wood Pile'           WHERE name = 'Wood Pile (Large)' AND pack_id = 'mana-seed';
UPDATE asset SET name = 'Open Basket'         WHERE name = 'Open Crate'        AND pack_id = 'mana-seed';
UPDATE asset SET name = 'Pelt'                WHERE name = 'Sign Board'        AND pack_id = 'mana-seed';
UPDATE asset SET name = 'Fallen Tree (Large)' WHERE name = 'Fallen Tree 2'     AND pack_id = 'mana-seed';

UPDATE asset SET name = 'Signpost (Stone)', category = 'prop'
WHERE name = 'Wood Sign Post' AND pack_id = 'mana-seed';

UPDATE asset SET name = 'Money Bag', category = 'prop'
WHERE name = 'Sunk Barrel' AND pack_id = 'mana-seed';

-- Undo category moves
UPDATE asset SET category = 'rock' WHERE name = 'River Rock' AND pack_id = 'mana-seed';
UPDATE asset SET category = 'stump'
WHERE category = 'stumps-and-logs' AND name NOT IN ('Fallen Tree', 'Fallen Tree 2', 'Wood Resource');
UPDATE asset SET category = 'tree'
WHERE category = 'stumps-and-logs' AND name IN ('Fallen Tree', 'Fallen Tree 2');
UPDATE asset SET category = 'prop'
WHERE category = 'stumps-and-logs' AND name = 'Wood Resource';
