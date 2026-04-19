-- ZBBS-065: per-asset stand offset for visible-when-inside structures.
--
-- Door offset picks the walkable tile an NPC targets when "going inside."
-- That's the right render position for a cottage (NPC disappears inside;
-- door tile wasn't going to be shown anyway) and an acceptable fallback
-- for a market stall (admin can hide the awkward door-standing by placing
-- the door behind the counter).
--
-- But it's not ideal: a fishmonger belongs BEHIND the counter, which is
-- inside the unwalkable footprint — you can't use that tile as a
-- pathfind target. The door stays as the walkable entry point; the new
-- stand_offset is a pure-render position the client uses when the NPC is
-- inside AND the asset is visible_when_inside. NULL falls back to door
-- position (current behavior).

BEGIN;

ALTER TABLE asset
    ADD COLUMN stand_offset_x INTEGER,
    ADD COLUMN stand_offset_y INTEGER;

COMMIT;
