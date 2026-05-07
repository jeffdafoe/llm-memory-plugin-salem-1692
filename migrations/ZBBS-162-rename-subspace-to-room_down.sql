-- Reverse of ZBBS-161-rename-subspace-to-room_up.sql. Reverts identifiers
-- to the original ZBBS-149 names. Roll back alongside the engine code
-- that uses room_* names.

BEGIN;

ALTER INDEX ix_actor_inside_room        RENAME TO ix_actor_inside_subspace;
ALTER INDEX ix_room_access_actor        RENAME TO ix_subspace_access_actor;
ALTER INDEX ix_structure_room_structure RENAME TO ix_structure_subspace_structure;

ALTER TABLE actor RENAME COLUMN inside_room_id TO inside_subspace_id;

ALTER TABLE room_access RENAME COLUMN room_id TO subspace_id;

ALTER TABLE room_access    RENAME TO subspace_access;
ALTER TABLE structure_room RENAME TO structure_subspace;

ALTER TYPE room_kind RENAME TO subspace_kind;

COMMIT;
