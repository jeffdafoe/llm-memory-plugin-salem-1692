-- ZBBS-032: Remove Hay Bale (32x32 sheet cell (96,32) is blank per visual check)
-- and the duplicate Large Crate on the 32x64 sheet (same name, same coords,
-- different UUID — clear data bug from ZBBS-018).

-- Hay Bale: blank cell
DELETE FROM asset_state
WHERE asset_id IN (SELECT id FROM asset WHERE name = 'Hay Bale' AND pack_id = 'mana-seed');
DELETE FROM asset WHERE name = 'Hay Bale' AND pack_id = 'mana-seed';

-- Duplicate Large Crate: 2 rows exist at (64,0) on the 32x64 sheet with 0 placements each.
-- Keep the one with the lowest UUID (deterministic), delete the other.
WITH ranked AS (
    SELECT a.id, ROW_NUMBER() OVER (ORDER BY a.id) AS rn
    FROM asset a
    JOIN asset_state s ON s.asset_id = a.id
    WHERE a.name = 'Large Crate'
      AND a.pack_id = 'mana-seed'
      AND s.sheet = '/tilesets/mana-seed/village-accessories/village accessories 32x64.png'
      AND s.src_x = 64 AND s.src_y = 0
)
DELETE FROM asset_state
WHERE asset_id IN (SELECT id FROM ranked WHERE rn > 1);

WITH ranked AS (
    SELECT a.id, ROW_NUMBER() OVER (ORDER BY a.id) AS rn
    FROM asset a
    WHERE a.name = 'Large Crate'
      AND a.pack_id = 'mana-seed'
      AND EXISTS (
          SELECT 1 FROM asset_state s
          WHERE s.asset_id = a.id
            AND s.sheet = '/tilesets/mana-seed/village-accessories/village accessories 32x64.png'
            AND s.src_x = 64 AND s.src_y = 0
      )
)
DELETE FROM asset WHERE id IN (SELECT id FROM ranked WHERE rn > 1);
