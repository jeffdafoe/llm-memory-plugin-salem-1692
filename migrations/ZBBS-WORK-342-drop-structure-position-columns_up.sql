-- ZBBS-WORK-342: drop structure.position_x / position_y.
--
-- These columns were a denormalized cache of the structure's tile anchor,
-- seeded once from village_object.x/y as floor-divided UNPADDED tiles by
-- migrations/ZBBS-WORK-240-structures-pg_up.sql. Nothing in the v2 engine
-- ever updated them after that — VillageObject.Pos (world-pixels, kept
-- current by /api/village/admin/object/move) was the actual source of
-- truth, and the Shared-Identity Bridge (every Structure.ID matches a
-- VillageObject.ID) makes the canonical tile anchor derivable on demand
-- via vobj.Pos.Tile() (which adds PadX/PadY for the wire-canonical
-- padded-tile frame; see shared/notes/codebase/salem/coordinate-frames).
--
-- Two engine consumers had been reading the stale unpadded copy as if it
-- were a padded TilePos (create_pc.go:109 lodging anchor;
-- huddle_commands.go:89 SceneBoundStructure origin) — both pad-drop bugs
-- AND staleness bugs whenever the editor moved a structure-backed
-- village_object. This change replaces those reads with
-- villageObjectForStructure(...).Pos.Tile() and drops the redundant
-- columns.
--
-- Discussion #110 originally voted for migrating position_x/position_y
-- to padded values, on the (incorrect) premise that an editor write path
-- existed that needed a pad-on-ingest seam. Verified 2026-05-28 that no
-- such write path exists, and even if it did, the column would re-stale
-- on every move. Dropping is the correct fix.

BEGIN;

ALTER TABLE public.structure
    DROP COLUMN position_x,
    DROP COLUMN position_y;

COMMIT;
