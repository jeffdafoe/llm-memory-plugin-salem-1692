-- Rollback ZBBS-059.

BEGIN;

DELETE FROM asset_state_tag WHERE tag IN ('laundry', 'notice-board');

DELETE FROM npc_behavior WHERE slug IN ('washerwoman', 'town_crier');

ALTER TABLE npc DROP CONSTRAINT IF EXISTS fk_npc_work_structure;
ALTER TABLE npc DROP CONSTRAINT IF EXISTS fk_npc_home_structure;

ALTER TABLE npc
    DROP COLUMN IF EXISTS work_structure_id,
    DROP COLUMN IF EXISTS home_structure_id;

COMMIT;
