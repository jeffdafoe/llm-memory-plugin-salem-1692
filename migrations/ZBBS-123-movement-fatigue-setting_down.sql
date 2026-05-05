-- ZBBS-123 down: remove the movement fatigue setting.
--
-- The Go helper applyMovementFatigue treats a missing row as
-- "default 12" via loadSetting's fallback, so deleting this row
-- doesn't disable fatigue — it returns calibration to whatever
-- the code-side default is. To actually disable fatigue without
-- removing the row, UPDATE setting SET value = '0' WHERE key =
-- 'movement_fatigue_per_tile_x100'.

BEGIN;

DELETE FROM setting WHERE key = 'movement_fatigue_per_tile_x100';

COMMIT;
