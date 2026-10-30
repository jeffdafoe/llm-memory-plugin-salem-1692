-- LLM-850 rollback: drop the snow cover.
--
-- Harmless to the village: the snow melts at once, and the trees and ground
-- show their clean winter art. Engine-STOPPED like the up (deploy.sh stop ->
-- migrate -> start).

BEGIN;

ALTER TABLE world_state DROP COLUMN IF EXISTS snow_cover_x100;

COMMIT;
