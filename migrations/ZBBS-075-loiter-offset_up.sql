-- ZBBS-075: per-instance loiter offset on village_object.
--
-- door_offset (on asset) is the walkable target where occupants and workers
-- enter. It's per-asset because a building's entrance is a property of the
-- template, not the placement.
--
-- loiter_offset is per-instance because where visitors stand outside a
-- building depends on the specific placement: which side faces the path,
-- where the path actually is, where adjacent buildings are. Two instances
-- of the same Mana Seed asset can have completely different loiter spots.
--
-- Units: tile-unit integers, same as door_offset_x/y. The agent move-to
-- resolver translates with `* 32.0` (the engine's tileSize). NULL means
-- "no loiter override — visitors fall back to door_offset, same as today."

BEGIN;

ALTER TABLE village_object
    ADD COLUMN loiter_offset_x INTEGER,
    ADD COLUMN loiter_offset_y INTEGER;

COMMIT;
