-- ZBBS-HOME-267 — revert the PC presence-cleanup setting row.

BEGIN;

DELETE FROM setting WHERE key = 'pc_presence_clear_minutes';

COMMIT;
