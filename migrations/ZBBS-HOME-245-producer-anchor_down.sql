-- ZBBS-HOME-245 down. Inventory seeds are kept (no good way to
-- distinguish seed-from-purchase). Coordinates are NOT restored to
-- a prior position (don't have a record of where they were before).

BEGIN;
-- No-op down. Reapply 245 if state needs reverting.
SELECT 1;
COMMIT;
