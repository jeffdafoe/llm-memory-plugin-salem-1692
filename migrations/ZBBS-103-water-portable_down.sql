-- ZBBS-103 down: revert water back to non-portable.

BEGIN;

UPDATE item_kind
   SET capabilities = array_remove(capabilities, 'portable')
 WHERE name = 'water';

COMMIT;
