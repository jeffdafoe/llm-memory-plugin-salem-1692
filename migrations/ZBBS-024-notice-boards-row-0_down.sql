-- ZBBS-024 down: remove Notice Board 1-5

DELETE FROM asset_state
WHERE asset_id IN (
    SELECT id FROM asset
    WHERE pack_id = 'mana-seed'
      AND name IN ('Notice Board 1', 'Notice Board 2', 'Notice Board 3', 'Notice Board 4', 'Notice Board 5')
);

DELETE FROM asset
WHERE pack_id = 'mana-seed'
  AND name IN ('Notice Board 1', 'Notice Board 2', 'Notice Board 3', 'Notice Board 4', 'Notice Board 5');
