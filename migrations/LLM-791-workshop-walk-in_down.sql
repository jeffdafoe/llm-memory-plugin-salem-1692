-- LLM-791 rollback: Lewis's Workshop back to owner-only.

BEGIN;

UPDATE village_object
   SET entry_policy = 'owner-only'
 WHERE id = '019e0e3c-56fb-71a2-96b3-0f0740b6077b'
   AND entry_policy = 'open';

COMMIT;
