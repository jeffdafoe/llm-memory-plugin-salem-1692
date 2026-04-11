-- ZBBS-007: Add location tracking to village agents

-- Location type: 'off-map', 'outdoor', 'inside'
ALTER TABLE village_agent ADD COLUMN location_type VARCHAR(10) NOT NULL DEFAULT 'off-map';
ALTER TABLE village_agent ADD COLUMN location_object_id UUID;
ALTER TABLE village_agent ADD COLUMN location_x DOUBLE PRECISION;
ALTER TABLE village_agent ADD COLUMN location_y DOUBLE PRECISION;

ALTER TABLE village_agent ADD CONSTRAINT fk_agent_location_object
    FOREIGN KEY (location_object_id) REFERENCES village_object (id) ON DELETE SET NULL;

CREATE INDEX idx_village_agent_location_object ON village_agent (location_object_id);
