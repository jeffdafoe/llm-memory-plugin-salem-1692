-- ZBBS-070 down: restore the binary "1+ inside = occupied" semantics
-- by dropping the per-asset threshold and night-only columns.

BEGIN;

ALTER TABLE asset
    DROP CONSTRAINT IF EXISTS chk_asset_occupied_min_count;

ALTER TABLE asset
    DROP COLUMN IF EXISTS occupied_min_count,
    DROP COLUMN IF EXISTS occupied_night_only;

COMMIT;
