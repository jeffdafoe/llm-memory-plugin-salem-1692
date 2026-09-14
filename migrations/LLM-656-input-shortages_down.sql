-- LLM-656 rollback: drop the standing-shortage record.
--
-- Harmless to the village: the sweep rebuilds the record from live inventory
-- one day at a time, so a rollback only delays the next peddler by
-- shortage_peddler_days. Engine-STOPPED like the up (deploy.sh stop -> migrate
-- -> start).

BEGIN;

ALTER TABLE world_state DROP COLUMN IF EXISTS input_shortages;

COMMIT;
