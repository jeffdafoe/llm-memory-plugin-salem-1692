-- ZBBS-095 down: revert the needs setting-key renames.

BEGIN;

UPDATE setting
   SET key = 'attribute_tick_amount'
 WHERE key = 'needs_tick_amount';

UPDATE setting
   SET key = 'last_attribute_tick_at'
 WHERE key = 'last_needs_tick_at';

COMMIT;
