-- ZBBS-111 down: drop the seeded return-to-work delay setting.
--
-- Conservative DELETE: matches the seeded default value exactly so a
-- rollback doesn't wipe an operator-customized override. If the row
-- has been edited (e.g. to '[45,90]'), the down migration leaves it
-- in place — operator can clean up manually if desired.

BEGIN;

DELETE FROM setting
WHERE key = 'return_to_work_delay_seconds'
  AND value = '[30,60]';

COMMIT;
