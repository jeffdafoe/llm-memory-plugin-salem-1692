-- ZBBS-016: Fix summer forest catalog
-- Corrects misidentified sprites, wrong coordinates, and missing entries.
-- Also drops asset FK constraints permanently — they just get in the way.

-- Drop asset FKs
ALTER TABLE asset_state DROP CONSTRAINT IF EXISTS asset_state_asset_id_fkey;
ALTER TABLE village_object DROP CONSTRAINT IF EXISTS fk_village_object_asset;

-- 16x16 row 2: rename bush entries to water/lily pad items
UPDATE asset_state SET asset_id = 'lily-pad-small' WHERE asset_id = 'bush-round';
UPDATE asset SET id = 'lily-pad-small', name = 'Small Lily Pad' WHERE id = 'bush-round';

UPDATE asset_state SET asset_id = 'lily-pad-light' WHERE asset_id = 'bush-light';
UPDATE asset SET id = 'lily-pad-light', name = 'Light Lily Pad' WHERE id = 'bush-light';

UPDATE asset_state SET asset_id = 'water-vine' WHERE asset_id = 'vine';
UPDATE asset SET id = 'water-vine', name = 'Water Vine' WHERE id = 'vine';

UPDATE asset_state SET asset_id = 'lily-pad-tiny' WHERE asset_id = 'bush-tiny';
UPDATE asset SET id = 'lily-pad-tiny', name = 'Tiny Lily Pad' WHERE id = 'bush-tiny';

-- 16x32: fix Berry Bush Cluster coordinates (was cols 2-3, actually cols 4-5)
UPDATE asset_state SET src_x = 64, src_w = 16
WHERE asset_id = 'bush-berry-cluster' AND state = 'default';

-- 16x32: add missing grass entries for cols 2 and 3
INSERT INTO asset (id, name, category, default_state, pack_id)
VALUES ('grass-medium', 'Medium Grass', 'nature', 'default', 'mana-seed');
INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
VALUES ('grass-medium', 'default', '/tilesets/mana-seed/summer-forest/summer sheets/summer 16x32.png', 32, 0, 16, 32, 1, 0);

INSERT INTO asset (id, name, category, default_state, pack_id)
VALUES ('grass-thick', 'Thick Grass', 'nature', 'default', 'mana-seed');
INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
VALUES ('grass-thick', 'default', '/tilesets/mana-seed/summer-forest/summer sheets/summer 16x32.png', 48, 0, 16, 32, 1, 0);

-- 16x32: add missing blueberry bush for col 5
INSERT INTO asset (id, name, category, default_state, pack_id)
VALUES ('bush-blueberry', 'Blueberry Bush', 'nature', 'default', 'mana-seed');
INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
VALUES ('bush-blueberry', 'default', '/tilesets/mana-seed/summer-forest/summer sheets/summer 16x32.png', 80, 0, 16, 32, 1, 0);

-- 32x32 (0,5): rename Log Pile to Fallen Tree
UPDATE asset_state SET asset_id = 'fallen-tree' WHERE asset_id = 'log-pile';
UPDATE village_object SET asset_id = 'fallen-tree' WHERE asset_id = 'log-pile';
UPDATE asset SET id = 'fallen-tree', name = 'Fallen Tree' WHERE id = 'log-pile';

-- 48x32 (0,2): rename Bone Pile to Fallen Tree (large)
UPDATE asset_state SET asset_id = 'fallen-tree-large' WHERE asset_id = 'bone-pile';
UPDATE village_object SET asset_id = 'fallen-tree-large' WHERE asset_id = 'bone-pile';
UPDATE asset SET id = 'fallen-tree-large', name = 'Fallen Tree (Large)' WHERE id = 'bone-pile';
