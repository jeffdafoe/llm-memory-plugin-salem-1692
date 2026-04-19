-- ZBBS-061: structure door offsets + NPC inside flag.
--
-- Home routing for scheduled behaviors (lamplighter, washerwoman, town_crier)
-- currently targets "nearest walkable tile adjacent to the home structure,"
-- which visually parks the villager outside the front wall regardless of where
-- the door actually is. Add per-asset door_offset_x / door_offset_y so each
-- structure type knows where its door is (tile offset from placement origin,
-- positive y = south). NULL = no door, routing falls back to the old adjacent-
-- tile behavior.
--
-- The new npc.inside flag says the villager has arrived home and is
-- indoors. The client hides the sprite while inside; the run-cycle trigger
-- (or the scheduled dispatch) flips it back on exit.

BEGIN;

ALTER TABLE asset
    ADD COLUMN door_offset_x INT,
    ADD COLUMN door_offset_y INT;

ALTER TABLE npc
    ADD COLUMN inside BOOLEAN NOT NULL DEFAULT false;

COMMIT;
