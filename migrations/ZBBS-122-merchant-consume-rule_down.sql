-- ZBBS-122 down: remove the consume-your-own-stock sentence from the
-- merchant role. REPLACE matches the exact appended text and is a
-- no-op if the sentence isn't present.

BEGIN;

UPDATE attribute_definition
   SET instructions = REPLACE(instructions, E'\n- When you eat from your own stock, ALWAYS call consume. Never narrate eating via act.', ''),
       updated_at = NOW()
 WHERE slug = 'merchant';

COMMIT;
