-- Reverse of ZBBS-150-sleep-redesign_up.sql.
--
-- Removes the two settings rows. Engine code that references these
-- (sleep.go, tiredness_recovery_sweep.go, touchPCInput) must be
-- rolled back alongside this migration. Defaults baked into Go
-- code (pc_sleep_max_duration_hours=12, pc_idle_sleep_min_tiredness=10)
-- mean the engine still runs without these rows, so order of rollback
-- isn't strict — but the post-rollback engine binary needs to predate
-- the new wake conditions / auto-bed gate.

BEGIN;

DELETE FROM setting
 WHERE key IN ('pc_sleep_max_duration_hours', 'pc_idle_sleep_min_tiredness');

COMMIT;
