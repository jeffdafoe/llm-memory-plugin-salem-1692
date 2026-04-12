-- ZBBS-017: Switch asset PK from string slug to UUID
-- The old string id becomes just the name field. All references updated.

-- Drop remaining FKs that reference asset.id
ALTER TABLE asset_slot DROP CONSTRAINT IF EXISTS asset_slot_asset_id_fkey;
ALTER TABLE asset DROP CONSTRAINT IF EXISTS asset_pack_id_fkey;

-- Step 1: Add UUID column to asset
ALTER TABLE asset ADD COLUMN new_id UUID DEFAULT gen_random_uuid();
UPDATE asset SET new_id = gen_random_uuid();

-- Step 2: Add UUID columns to referencing tables
ALTER TABLE asset_state ADD COLUMN new_asset_id UUID;
ALTER TABLE asset_slot ADD COLUMN new_asset_id UUID;
ALTER TABLE village_object ADD COLUMN new_asset_id UUID;

-- Step 3: Populate UUID refs by joining on old string id
UPDATE asset_state s SET new_asset_id = a.new_id FROM asset a WHERE s.asset_id = a.id;
UPDATE asset_slot s SET new_asset_id = a.new_id FROM asset a WHERE s.asset_id = a.id;
UPDATE village_object o SET new_asset_id = a.new_id FROM asset a WHERE o.asset_id = a.id;

-- Step 4: Drop old columns, rename new ones
-- asset: drop old PK, rename new_id to id
ALTER TABLE asset DROP CONSTRAINT asset_pkey;
ALTER TABLE asset DROP COLUMN id;
ALTER TABLE asset RENAME COLUMN new_id TO id;
ALTER TABLE asset ADD PRIMARY KEY (id);

-- asset_state: drop old asset_id, rename
DROP INDEX IF EXISTS idx_asset_state_asset;
ALTER TABLE asset_state DROP CONSTRAINT IF EXISTS asset_state_asset_id_state_key;
ALTER TABLE asset_state DROP COLUMN asset_id;
ALTER TABLE asset_state RENAME COLUMN new_asset_id TO asset_id;
ALTER TABLE asset_state ADD CONSTRAINT asset_state_asset_id_state_key UNIQUE (asset_id, state);
CREATE INDEX idx_asset_state_asset ON asset_state(asset_id);

-- asset_slot: drop old asset_id, rename
DROP INDEX IF EXISTS idx_asset_slot_asset_id;
ALTER TABLE asset_slot DROP CONSTRAINT IF EXISTS asset_slot_asset_id_slot_name_key;
ALTER TABLE asset_slot DROP COLUMN asset_id;
ALTER TABLE asset_slot RENAME COLUMN new_asset_id TO asset_id;
ALTER TABLE asset_slot ADD CONSTRAINT asset_slot_asset_id_slot_name_key UNIQUE (asset_id, slot_name);
CREATE INDEX idx_asset_slot_asset_id ON asset_slot(asset_id);

-- village_object: drop old asset_id, rename
DROP INDEX IF EXISTS idx_village_object_asset;
ALTER TABLE village_object DROP COLUMN asset_id;
ALTER TABLE village_object RENAME COLUMN new_asset_id TO asset_id;
CREATE INDEX idx_village_object_asset ON village_object(asset_id);

-- Step 5: Add source_file column
ALTER TABLE asset ADD COLUMN source_file VARCHAR(200) DEFAULT NULL;

-- Step 6: Drop fits_slot index and recreate for UUID type
DROP INDEX IF EXISTS idx_asset_fits_slot;
CREATE INDEX idx_asset_fits_slot ON asset(fits_slot) WHERE fits_slot IS NOT NULL;

-- Step 7: Make asset_id NOT NULL on referencing tables
ALTER TABLE asset_state ALTER COLUMN asset_id SET NOT NULL;
ALTER TABLE asset_slot ALTER COLUMN asset_id SET NOT NULL;
