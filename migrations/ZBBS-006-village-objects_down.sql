-- Revert ZBBS-006: drop village_object, restore village_building tables

DROP TABLE IF EXISTS village_object;

-- Restore original building tables
CREATE TABLE village_building (
    id UUID NOT NULL DEFAULT gen_random_uuid(),
    tile_x INT NOT NULL,
    tile_y INT NOT NULL,
    building_style VARCHAR(30) NOT NULL,
    building_variant INT NOT NULL DEFAULT 1,
    created_at TIMESTAMP(0) WITHOUT TIME ZONE NOT NULL DEFAULT NOW(),
    PRIMARY KEY (id)
);
CREATE INDEX idx_village_building_position ON village_building (tile_x, tile_y);

CREATE TABLE village_building_resident (
    id UUID NOT NULL DEFAULT gen_random_uuid(),
    building_id UUID NOT NULL,
    agent_id UUID NOT NULL,
    moved_in_at TIMESTAMP(0) WITHOUT TIME ZONE NOT NULL DEFAULT NOW(),
    PRIMARY KEY (id)
);
ALTER TABLE village_building_resident ADD CONSTRAINT fk_resident_building FOREIGN KEY (building_id) REFERENCES village_building (id) NOT DEFERRABLE;
ALTER TABLE village_building_resident ADD CONSTRAINT fk_resident_agent FOREIGN KEY (agent_id) REFERENCES village_agent (id) NOT DEFERRABLE;
CREATE UNIQUE INDEX idx_resident_unique ON village_building_resident (building_id, agent_id);
CREATE INDEX idx_resident_agent ON village_building_resident (agent_id);
