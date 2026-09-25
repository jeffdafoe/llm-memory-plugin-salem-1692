-- LLM-677: public works slice 3 — a fallen tree blocks a road.
--
-- A road break places one of the catalog's fallen trees across a road (engine
-- damage_road.go) and the repair removes it. The engine names the two assets
-- by id (sim.roadObstacleAssets):
--   1d0accaa-dc6b-4ab1-8d3d-ad3076b5efd5  Fallen Log     (summer 48x32.png, x 48)
--   6e894192-421d-45a0-b67c-d34d8f0f5885  Fallen Tree 2  (summer 48x32.png, x 96)
--
-- WHAT this changes: both become obstacles with a footprint the size of the
-- drawn log. Each sprite is 48x32 at render scale 2 — 3 tiles wide, 2 tall —
-- anchored at (0.5, 0.85), so it covers the anchor row and the row above it.
-- Before this, Fallen Log was not an obstacle at all and both blocked only
-- their centre tile, so a walker would be drawn stepping through the log.
--   is_obstacle = true, footprint left 1 / right 1 / top 1 / bottom 0.
-- Neither asset is placed anywhere in the live village, so no existing object
-- changes shape.
--
-- Both rows exist only in the live catalog (assets were authored in the
-- editor); on a fresh DB or the pg integration harness the UPDATE touches
-- nothing and the engine places no road obstacle — the same posture as a
-- catalog without the Debris asset.
--
-- asset is reference data (boot-loaded). deploy.sh runs stop -> migrate ->
-- start, so the change lands on the restart.
--
-- Rerun-safe: the UPDATE is idempotent. Loud validation at the end.

BEGIN;

UPDATE asset
   SET is_obstacle = true,
       footprint_left = 1,
       footprint_right = 1,
       footprint_top = 1,
       footprint_bottom = 0
 WHERE id IN ('1d0accaa-dc6b-4ab1-8d3d-ad3076b5efd5', '6e894192-421d-45a0-b67c-d34d8f0f5885');

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM asset
                WHERE id IN ('1d0accaa-dc6b-4ab1-8d3d-ad3076b5efd5', '6e894192-421d-45a0-b67c-d34d8f0f5885')
                  AND NOT (is_obstacle AND footprint_left = 1 AND footprint_right = 1
                           AND footprint_top = 1 AND footprint_bottom = 0)) THEN
        RAISE EXCEPTION 'LLM-677: a fallen-tree asset does not carry the 3x2 obstacle footprint';
    END IF;
END $$;

COMMIT;
