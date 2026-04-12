-- ZBBS-016 rollback

-- Restore bone-pile
UPDATE asset_state SET asset_id = 'bone-pile' WHERE asset_id = 'fallen-tree-large';
UPDATE village_object SET asset_id = 'bone-pile' WHERE asset_id = 'fallen-tree-large';
UPDATE asset SET id = 'bone-pile', name = 'Bone Pile' WHERE id = 'fallen-tree-large';

-- Restore log-pile
UPDATE asset_state SET asset_id = 'log-pile' WHERE asset_id = 'fallen-tree';
UPDATE village_object SET asset_id = 'log-pile' WHERE asset_id = 'fallen-tree';
UPDATE asset SET id = 'log-pile', name = 'Log Pile' WHERE id = 'fallen-tree';

-- Remove added assets
DELETE FROM asset_state WHERE asset_id = 'bush-blueberry';
DELETE FROM asset WHERE id = 'bush-blueberry';
DELETE FROM asset_state WHERE asset_id = 'grass-thick';
DELETE FROM asset WHERE id = 'grass-thick';
DELETE FROM asset_state WHERE asset_id = 'grass-medium';
DELETE FROM asset WHERE id = 'grass-medium';

-- Restore Berry Bush Cluster coordinates
UPDATE asset_state SET src_x = 32, src_w = 32
WHERE asset_id = 'bush-berry-cluster' AND state = 'default';

-- Restore bush names
UPDATE asset_state SET asset_id = 'bush-tiny' WHERE asset_id = 'lily-pad-tiny';
UPDATE asset SET id = 'bush-tiny', name = 'Tiny Bush' WHERE id = 'lily-pad-tiny';

UPDATE asset_state SET asset_id = 'vine' WHERE asset_id = 'water-vine';
UPDATE asset SET id = 'vine', name = 'Vine' WHERE id = 'water-vine';

UPDATE asset_state SET asset_id = 'bush-light' WHERE asset_id = 'lily-pad-light';
UPDATE asset SET id = 'bush-light', name = 'Light Bush' WHERE id = 'lily-pad-light';

UPDATE asset_state SET asset_id = 'bush-round' WHERE asset_id = 'lily-pad-small';
UPDATE asset SET id = 'bush-round', name = 'Round Bush' WHERE id = 'lily-pad-small';

-- Restore FKs
ALTER TABLE asset_state ADD CONSTRAINT asset_state_asset_id_fkey
    FOREIGN KEY (asset_id) REFERENCES asset(id) ON DELETE CASCADE;
ALTER TABLE village_object ADD CONSTRAINT fk_village_object_asset
    FOREIGN KEY (asset_id) REFERENCES asset(id);
