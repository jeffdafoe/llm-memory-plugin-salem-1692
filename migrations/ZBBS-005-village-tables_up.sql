-- ZBBS-005: Village agent, building, and resident tables

-- Village agents — links llm-memory agents to the village world
CREATE TABLE village_agent (
    id UUID NOT NULL DEFAULT gen_random_uuid(),
    name VARCHAR(100) NOT NULL,
    llm_memory_agent VARCHAR(100) NOT NULL,
    llm_memory_api_key VARCHAR(255) NOT NULL,
    role VARCHAR(50) NOT NULL,
    coins INT NOT NULL DEFAULT 100,
    is_virtual BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMP(0) WITHOUT TIME ZONE NOT NULL DEFAULT NOW(),
    PRIMARY KEY (id)
);
CREATE UNIQUE INDEX idx_village_agent_name ON village_agent (name);
CREATE UNIQUE INDEX idx_village_agent_llm ON village_agent (llm_memory_agent);

-- Village buildings — houses and structures on the map
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

-- Building residents — who lives where (multiple agents per building)
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
