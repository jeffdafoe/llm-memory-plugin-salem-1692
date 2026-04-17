-- ZBBS-022 down: Remove Black buildings

DELETE FROM asset_state
WHERE asset_id IN (
    SELECT id FROM asset
    WHERE pack_id = 'tiny-swords'
      AND name LIKE 'Black %'
);

DELETE FROM asset
WHERE pack_id = 'tiny-swords'
  AND name LIKE 'Black %';
