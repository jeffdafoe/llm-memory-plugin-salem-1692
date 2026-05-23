-- Rollback ZBBS-HOME-296 PR1 config — remove the `lodging` capability
-- token from nights_stay. Leaves `service` (the prod-baseline token,
-- ZBBS-131) intact. Idempotent.

BEGIN;

UPDATE item_kind
   SET capabilities = array_remove(capabilities, 'lodging')
 WHERE name = 'nights_stay';

COMMIT;
