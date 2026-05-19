-- Rollback ZBBS-WORK-240. Reverses the structure-table creation +
-- structure_room reshape + actor column retypes.
--
-- Caveats:
--   * Down rolls actor.{home,work,inside}_structure_id back to UUID.
--     ANY non-UUID-shaped value (e.g. a heterogeneous v2 StructureID
--     introduced after this migration shipped) will fail the cast.
--     Operator must purge those rows or accept this is destructive.
--   * actor.inside_room_id stays BIGINT NULL (FK is re-added).
--   * room_access remains untouched (CASCADE FK to structure_room stays).

BEGIN;

-- Step 9 reverse: actor.{home,work,inside}_structure_id TEXT → UUID + re-add FKs.
ALTER TABLE actor ALTER COLUMN home_structure_id   TYPE UUID USING home_structure_id::uuid;
ALTER TABLE actor ALTER COLUMN work_structure_id   TYPE UUID USING work_structure_id::uuid;
ALTER TABLE actor ALTER COLUMN inside_structure_id TYPE UUID USING inside_structure_id::uuid;

ALTER TABLE actor
    ADD CONSTRAINT actor_home_structure_id_fkey
        FOREIGN KEY (home_structure_id) REFERENCES village_object(id) ON DELETE SET NULL,
    ADD CONSTRAINT actor_work_structure_id_fkey
        FOREIGN KEY (work_structure_id) REFERENCES village_object(id) ON DELETE SET NULL,
    ADD CONSTRAINT actor_inside_structure_id_fkey
        FOREIGN KEY (inside_structure_id) REFERENCES village_object(id) ON DELETE SET NULL;

-- Step 8 reverse: re-add actor.inside_room_id FK to structure_room.
ALTER TABLE actor
    ADD CONSTRAINT actor_inside_room_id_fkey
        FOREIGN KEY (inside_room_id) REFERENCES structure_room(id);

-- Step 7 reverse: drop CHECKs + gen-marker on structure_room.
ALTER TABLE structure_room
    DROP CONSTRAINT IF EXISTS structure_room_name_nonempty,
    DROP CONSTRAINT IF EXISTS structure_room_structure_id_nonempty,
    DROP CONSTRAINT IF EXISTS structure_room_id_positive;

DROP SEQUENCE IF EXISTS structure_room_snapshot_gen_seq;
DROP INDEX IF EXISTS idx_structure_room_snapshot_gen;
ALTER TABLE structure_room DROP COLUMN IF EXISTS snapshot_gen;

-- Step 6 reverse: drop new FK to structure(id).
ALTER TABLE structure_room DROP CONSTRAINT IF EXISTS structure_room_structure_id_fkey;

-- Step 5 reverse: retype structure_room.structure_id TEXT → UUID.
ALTER TABLE structure_room ALTER COLUMN structure_id TYPE UUID USING structure_id::uuid;

-- Step 4 reverse: re-add structure_room → village_object FK.
ALTER TABLE structure_room
    ADD CONSTRAINT structure_room_structure_id_fkey
        FOREIGN KEY (structure_id) REFERENCES village_object(id) ON DELETE CASCADE;

-- Steps 1 + 2 reverse: drop structure table + its sequence.
DROP SEQUENCE IF EXISTS structure_snapshot_gen_seq;
DROP TABLE IF EXISTS structure;

COMMIT;
