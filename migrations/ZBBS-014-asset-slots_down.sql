-- ZBBS-014 rollback
DROP INDEX IF EXISTS idx_village_object_attached_to;
DROP INDEX IF EXISTS idx_asset_fits_slot;
DROP INDEX IF EXISTS idx_asset_slot_asset_id;
ALTER TABLE village_object DROP COLUMN IF EXISTS attached_to;
ALTER TABLE asset DROP COLUMN IF EXISTS fits_slot;
DROP TABLE IF EXISTS asset_slot;
