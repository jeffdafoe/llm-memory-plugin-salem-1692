-- Reverse of ZBBS-141-tiredness-recovery-tracking_up.sql.
--
-- Drops last_tiredness_recovery_at. The runTirednessRecoverySweep
-- goroutine in code references this column; rolling back this migration
-- requires reverting the engine binary alongside it. The pre-ZBBS-141
-- needs_tick recovery branch handled the in-tick case once the column
-- is gone.

ALTER TABLE actor DROP COLUMN last_tiredness_recovery_at;
