-- ZBBS-019 rollback: Remove Tiny Swords building pack

DELETE FROM asset_state WHERE asset_id IN (
    'house-1', 'house-2', 'house-3', 'castle', 'tower', 'barracks', 'archery', 'monastery'
);

DELETE FROM asset WHERE pack_id = 'tiny-swords';

DELETE FROM tileset_pack WHERE id = 'tiny-swords';
