-- ZBBS-037: Big prop catalog correction pass. Renames + category moves + a few
-- deletions based on visual review. Includes new 'stumps-and-logs' category.
-- All items verified 0 placements OR only touched by rename/category-move
-- (UUID FKs survive both). No placements break.

-- ============================================================================
-- Part 1: Flower Pot cascade (Mixed->Red, Red->Violet, Violet->Empty,
-- Small Barrel->Mixed). Two-step via temp names to avoid same-name collisions.
-- ============================================================================

UPDATE asset SET name = '__tmp_fp_mixed' WHERE name = 'Flower Pot (Mixed)' AND pack_id = 'mana-seed';
UPDATE asset SET name = '__tmp_fp_red'   WHERE name = 'Flower Pot (Red)'   AND pack_id = 'mana-seed';
UPDATE asset SET name = '__tmp_fp_viol'  WHERE name = 'Flower Pot (Violet)' AND pack_id = 'mana-seed';
UPDATE asset SET name = '__tmp_smlbar'   WHERE name = 'Small Barrel'       AND pack_id = 'mana-seed';

UPDATE asset SET name = 'Flower Pot (Red)'    WHERE name = '__tmp_fp_mixed';
UPDATE asset SET name = 'Flower Pot (Violet)' WHERE name = '__tmp_fp_red';
UPDATE asset SET name = 'Flower Pot (Empty)'  WHERE name = '__tmp_fp_viol';
UPDATE asset SET name = 'Flower Pot (Mixed)'  WHERE name = '__tmp_smlbar';

-- ============================================================================
-- Part 2: Small Pot cascade (Crystal Ball -> Small Pot (Empty); Small Pot -> Small Pot (Full))
-- Same two-step pattern because 'Small Pot' exists before we rename Crystal Ball.
-- ============================================================================

UPDATE asset SET name = '__tmp_sp_ball'  WHERE name = 'Crystal Ball' AND pack_id = 'mana-seed';
UPDATE asset SET name = '__tmp_sp_orig'  WHERE name = 'Small Pot'   AND pack_id = 'mana-seed';

UPDATE asset SET name = 'Small Pot (Empty)' WHERE name = '__tmp_sp_ball';
UPDATE asset SET name = 'Small Pot (Full)'  WHERE name = '__tmp_sp_orig';

-- ============================================================================
-- Part 3: Simple renames (no cascade)
-- ============================================================================

UPDATE asset SET name = 'Bucket (Empty)'   WHERE name = 'Blue Pot'         AND pack_id = 'mana-seed';
UPDATE asset SET name = 'Wood Pile (Small)' WHERE name = 'Scroll'          AND pack_id = 'mana-seed';
UPDATE asset SET name = 'Wood Pile (Large)' WHERE name = 'Wood Pile'       AND pack_id = 'mana-seed';
UPDATE asset SET name = 'Open Crate'       WHERE name = 'Open Basket'      AND pack_id = 'mana-seed';
UPDATE asset SET name = 'Sign Board'       WHERE name = 'Pelt'             AND pack_id = 'mana-seed';
UPDATE asset SET name = 'Fallen Tree 2'    WHERE name = 'Fallen Tree (Large)' AND pack_id = 'mana-seed';

-- Rename + recategorize: Signpost (Stone) -> Wood Sign Post, category 'sign'
UPDATE asset SET name = 'Wood Sign Post', category = 'sign'
WHERE name = 'Signpost (Stone)' AND pack_id = 'mana-seed';

-- Rename + recategorize: Money Bag -> Sunk Barrel, category 'water-features'
UPDATE asset SET name = 'Sunk Barrel', category = 'water-features'
WHERE name = 'Money Bag' AND pack_id = 'mana-seed';

-- ============================================================================
-- Part 4: Category move — River Rock: rock -> water-features
-- ============================================================================

UPDATE asset SET category = 'water-features'
WHERE name = 'River Rock' AND pack_id = 'mana-seed';

-- ============================================================================
-- Part 5: Consolidate all stumps + fallen trees + wood resource into
-- new 'stumps-and-logs' category. Removes the standalone 'stump' category.
-- ============================================================================

UPDATE asset SET category = 'stumps-and-logs'
WHERE category = 'stump';

UPDATE asset SET category = 'stumps-and-logs'
WHERE name IN ('Fallen Tree', 'Fallen Tree 2')
  AND pack_id = 'mana-seed';

UPDATE asset SET category = 'stumps-and-logs'
WHERE name = 'Wood Resource' AND pack_id = 'tiny-swords';

-- ============================================================================
-- Part 6: Deletions (both 0 placements, verified)
-- ============================================================================

DELETE FROM asset_state WHERE asset_id IN (
    SELECT id FROM asset
    WHERE (name = 'Rubber Duck'     AND pack_id = 'tiny-swords')
       OR (name = 'Signpost (Wood)' AND pack_id = 'mana-seed')
);

DELETE FROM asset
WHERE (name = 'Rubber Duck'     AND pack_id = 'tiny-swords')
   OR (name = 'Signpost (Wood)' AND pack_id = 'mana-seed');
