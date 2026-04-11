ALTER TABLE village_agent DROP CONSTRAINT IF EXISTS fk_agent_location_object;
ALTER TABLE village_agent DROP COLUMN IF EXISTS location_type;
ALTER TABLE village_agent DROP COLUMN IF EXISTS location_object_id;
ALTER TABLE village_agent DROP COLUMN IF EXISTS location_x;
ALTER TABLE village_agent DROP COLUMN IF EXISTS location_y;
