-- ZBBS-161: rename subspace → room across schema.
--
-- "Subspace" was the original ZBBS-149 name for first-class rooms-within-
-- a-structure. Settled on "room" as the simpler/clearer term before the
-- ZBBS-162 invariant work calcified more references. Pure rename — no
-- schema or behavior change beyond identifiers.
--
-- The engine commit lands the matching code-side rename; this migration
-- and that code must deploy together (engine restart after migrate).

BEGIN;

ALTER TYPE subspace_kind RENAME TO room_kind;

ALTER TABLE structure_subspace RENAME TO structure_room;
ALTER TABLE subspace_access    RENAME TO room_access;

ALTER TABLE room_access RENAME COLUMN subspace_id TO room_id;

ALTER TABLE actor RENAME COLUMN inside_subspace_id TO inside_room_id;

ALTER INDEX ix_structure_subspace_structure RENAME TO ix_structure_room_structure;
ALTER INDEX ix_subspace_access_actor        RENAME TO ix_room_access_actor;
ALTER INDEX ix_actor_inside_subspace        RENAME TO ix_actor_inside_room;

COMMIT;
