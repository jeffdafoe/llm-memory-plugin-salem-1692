-- LLM-843 rollback: drop the omen record.
--
-- Harmless to the village: an armed omen is dropped and every cooldown is
-- forgotten, so the next omen may land on anyone. Engine-STOPPED like the up
-- (deploy.sh stop -> migrate -> start).

BEGIN;

ALTER TABLE world_state DROP COLUMN IF EXISTS omens;

COMMIT;
