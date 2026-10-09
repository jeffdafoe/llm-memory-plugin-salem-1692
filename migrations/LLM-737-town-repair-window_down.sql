-- LLM-737 down: drop the in-flight town repair columns. A hand mid-repair
-- loses the window, as every restart did before. Same engine-stopped caveat
-- as the up.
--
-- Rerun-safe via IF EXISTS.

BEGIN;

ALTER TABLE actor DROP COLUMN IF EXISTS town_repair_object_id;
ALTER TABLE actor DROP COLUMN IF EXISTS town_repair_bounty;
ALTER TABLE actor DROP COLUMN IF EXISTS town_repair_started_at;
ALTER TABLE actor DROP COLUMN IF EXISTS town_repair_until;

COMMIT;
