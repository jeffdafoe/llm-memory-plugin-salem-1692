-- LLM-817 down: drop actor.closed_until. Same engine-stopped caveat as the up.

BEGIN;

ALTER TABLE actor DROP COLUMN IF EXISTS closed_until;

COMMIT;
