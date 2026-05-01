-- Down for ZBBS-087 — drop the village_event table and its index.

BEGIN;

DROP INDEX IF EXISTS village_event_recent_idx;
DROP TABLE IF EXISTS village_event;

COMMIT;
