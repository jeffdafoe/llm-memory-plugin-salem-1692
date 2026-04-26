BEGIN;

DROP INDEX IF EXISTS idx_pc_position_character;
ALTER TABLE pc_position
    DROP COLUMN IF EXISTS home_structure_id,
    DROP COLUMN IF EXISTS character_name;

COMMIT;
