-- ZBBS-180 down — strip the lodging tag off tavern-tagged structures.
--
-- Removes lodging only from rows that ALSO carry the tavern tag, so a
-- pure inn (lodging-only, never had tavern) keeps its tag intact.

BEGIN;

DELETE FROM village_object_tag a
 WHERE a.tag = 'lodging'
   AND EXISTS (
       SELECT 1 FROM village_object_tag b
        WHERE b.object_id = a.object_id
          AND b.tag = 'tavern'
   );

COMMIT;
