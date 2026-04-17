-- ZBBS-021 down: Remove red/yellow buildings; move blue back to flat layout

-- Delete red/yellow asset_state rows (cascades via asset_id FK would be ideal but explicit is safer)
DELETE FROM asset_state
WHERE asset_id IN (
    SELECT id FROM asset
    WHERE pack_id = 'tiny-swords'
      AND (name LIKE 'Red %' OR name LIKE 'Yellow %')
);

-- Delete red/yellow asset rows
DELETE FROM asset
WHERE pack_id = 'tiny-swords'
  AND (name LIKE 'Red %' OR name LIKE 'Yellow %');

-- Move blue sheet paths back to flat layout
UPDATE asset_state
SET sheet = REPLACE(sheet, '/tilesets/tiny-swords/buildings/blue/', '/tilesets/tiny-swords/buildings/')
WHERE sheet LIKE '/tilesets/tiny-swords/buildings/blue/%.png';

-- Strip "Blue " prefix and blue/ subdir from blue asset rows
UPDATE asset SET
    name = SUBSTRING(name FROM 6),
    source_file = SUBSTRING(source_file FROM 6)
WHERE pack_id = 'tiny-swords'
  AND name LIKE 'Blue %';
