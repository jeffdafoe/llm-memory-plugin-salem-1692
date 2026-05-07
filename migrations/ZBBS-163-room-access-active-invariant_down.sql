-- Reverse of ZBBS-163-room-access-active-invariant_up.sql. Drops the
-- partial unique index and the kind/active columns. Engine code that
-- reads/writes those columns must be rolled back alongside.

BEGIN;

DROP INDEX IF EXISTS ux_room_access_one_private_active;

ALTER TABLE room_access DROP COLUMN IF EXISTS active;
ALTER TABLE room_access DROP COLUMN IF EXISTS kind;

COMMIT;
