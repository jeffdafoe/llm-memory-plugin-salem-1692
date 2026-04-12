-- ZBBS-014: Asset attachment slot system
-- Supports overlay/composite objects (e.g., cooking pot on campfire, sign on signpost)
-- Base assets define named slots with pixel offsets; overlay assets declare which slot they fit.

-- Slot definitions on base assets
CREATE TABLE asset_slot (
    id SERIAL PRIMARY KEY,
    asset_id VARCHAR(64) NOT NULL REFERENCES asset(id) ON DELETE CASCADE,
    slot_name VARCHAR(32) NOT NULL,
    offset_x INT NOT NULL DEFAULT 0,
    offset_y INT NOT NULL DEFAULT 0,
    UNIQUE(asset_id, slot_name)
);

-- Overlay assets declare which slot type they fit
ALTER TABLE asset ADD COLUMN fits_slot VARCHAR(32) DEFAULT NULL;

-- Placed world objects can attach to a parent object
ALTER TABLE village_object ADD COLUMN attached_to UUID DEFAULT NULL REFERENCES village_object(id) ON DELETE CASCADE;

CREATE INDEX idx_asset_slot_asset_id ON asset_slot(asset_id);
CREATE INDEX idx_asset_fits_slot ON asset(fits_slot) WHERE fits_slot IS NOT NULL;
CREATE INDEX idx_village_object_attached_to ON village_object(attached_to) WHERE attached_to IS NOT NULL;
