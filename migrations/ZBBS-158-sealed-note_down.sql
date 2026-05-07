-- ZBBS-158 down: drop sealed_note table.

BEGIN;
DROP TABLE IF EXISTS sealed_note;
COMMIT;
