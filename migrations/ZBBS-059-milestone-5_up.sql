-- ZBBS-059: Milestone 5 — washerwoman + town_crier behaviors, plus
-- NPC-to-structure linkage (home + work).
--
-- Structure linkage: each NPC gets two nullable FKs to village_object.
-- home_structure_id grounds the NPC's home in a real building (today's
-- npc.home_x/y become a fallback for villagers without an assigned house).
-- work_structure_id is data-only for now; no behavior consumes it yet.
--
-- Tags: laundry assets and the Notice Board asset get behavior-specific tags
-- on their rotatable states so the washerwoman / town_crier can query their
-- own candidate pools, and so daily rotation can skip them when the NPC is
-- on duty.

BEGIN;

ALTER TABLE npc
    ADD COLUMN home_structure_id UUID NULL,
    ADD COLUMN work_structure_id UUID NULL;

ALTER TABLE npc
    ADD CONSTRAINT fk_npc_home_structure
        FOREIGN KEY (home_structure_id) REFERENCES village_object(id)
        ON DELETE SET NULL;

ALTER TABLE npc
    ADD CONSTRAINT fk_npc_work_structure
        FOREIGN KEY (work_structure_id) REFERENCES village_object(id)
        ON DELETE SET NULL;

-- Two new behaviors the editor dropdown will pick up automatically.
INSERT INTO npc_behavior (slug, display_name) VALUES
    ('washerwoman', 'Washerwoman'),
    ('town_crier',  'Town Crier');

-- Tag the rotatable states of the laundry assets. A single state can carry
-- multiple tags — these states remain tagged 'rotatable' too, which keeps
-- them in the bulk rotation pool for when no washerwoman is assigned.
INSERT INTO asset_state_tag (state_id, tag)
SELECT s.id, 'laundry'
FROM asset_state s
JOIN asset a ON s.asset_id = a.id
WHERE a.name LIKE 'Laundry%';

-- Tag the rotatable states of the Notice Board for the town_crier.
INSERT INTO asset_state_tag (state_id, tag)
SELECT s.id, 'notice-board'
FROM asset_state s
JOIN asset a ON s.asset_id = a.id
WHERE a.name = 'Notice Board';

COMMIT;
