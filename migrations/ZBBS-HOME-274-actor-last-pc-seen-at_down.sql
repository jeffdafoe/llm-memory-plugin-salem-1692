-- ZBBS-HOME-274 down — drop the client-liveness column.

BEGIN;

ALTER TABLE actor
    DROP COLUMN IF EXISTS last_pc_seen_at;

COMMIT;
