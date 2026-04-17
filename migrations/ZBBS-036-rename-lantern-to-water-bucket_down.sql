UPDATE asset SET name = 'Lantern'
WHERE name = 'Water Bucket'
  AND pack_id = 'mana-seed'
  AND id IN (
      SELECT asset_id FROM asset_state
      WHERE sheet = '/tilesets/mana-seed/village-accessories/village accessories 16x16.png'
        AND src_x = 144 AND src_y = 32
  );
