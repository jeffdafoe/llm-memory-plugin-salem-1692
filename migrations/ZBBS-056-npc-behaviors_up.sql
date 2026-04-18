-- ZBBS-056: npc_behavior lookup table
--
-- Data-driven list of valid values for npc.behavior. The editor's behavior
-- dropdown reads this via GET /api/village/npc-behaviors. Future behaviors
-- (washerwoman, town_crier) get seeded by their own feature migrations.

BEGIN;

CREATE TABLE npc_behavior (
    slug VARCHAR(64) PRIMARY KEY,
    display_name VARCHAR(100) NOT NULL
);

INSERT INTO npc_behavior (slug, display_name) VALUES
    ('lamplighter', 'Lamplighter');

-- Optional FK: enforce that npc.behavior references this table. Nullable
-- behavior stays allowed. ON UPDATE CASCADE so a slug rename auto-propagates.
ALTER TABLE npc
    ADD CONSTRAINT fk_npc_behavior
    FOREIGN KEY (behavior) REFERENCES npc_behavior (slug)
    ON UPDATE CASCADE ON DELETE SET NULL
    NOT DEFERRABLE;

COMMIT;
