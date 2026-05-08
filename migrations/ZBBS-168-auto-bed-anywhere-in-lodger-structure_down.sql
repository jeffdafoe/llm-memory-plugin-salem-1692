-- ZBBS-168 down: revert pc_idle_sleep_minutes from 15 back to 5,
-- but only if the row still holds the value this migration set.
-- An admin override past 15 is preserved.

BEGIN;

UPDATE setting
   SET value = '5'
 WHERE key = 'pc_idle_sleep_minutes'
   AND value = '15';

COMMIT;
