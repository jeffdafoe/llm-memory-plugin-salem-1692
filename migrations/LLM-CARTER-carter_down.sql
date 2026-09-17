-- Carter rollback: drop the cooldown anchor.
--
-- Harmless to the village: with the column gone the engine reads "no carter has
-- come yet" and the next residue run is at most one cooldown early. Engine-STOPPED
-- like the up (deploy.sh stop -> migrate -> start).

BEGIN;

ALTER TABLE world_state DROP COLUMN IF EXISTS last_carter_at;

COMMIT;
