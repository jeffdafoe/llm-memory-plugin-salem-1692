-- ZBBS-036: Rename "Lantern" to "Water Bucket" — the sprite at (144, 32) on the
-- 16x16 sheet depicts a bucket of water, not a lantern. ZBBS-018 guessed wrong.
-- 0 placements, but using a precise WHERE clause to avoid touching Hanging Lantern.

UPDATE asset SET name = 'Water Bucket'
WHERE name = 'Lantern'
  AND pack_id = 'mana-seed'
  AND id IN (
      SELECT asset_id FROM asset_state
      WHERE sheet = '/tilesets/mana-seed/village-accessories/village accessories 16x16.png'
        AND src_x = 144 AND src_y = 32
  );
