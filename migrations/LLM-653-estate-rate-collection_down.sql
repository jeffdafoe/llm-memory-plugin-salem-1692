-- LLM-653 rollback: drop the once-a-day assessment stamp.
--
-- Harmless to the coin: the stamp only gates a second collection in the same
-- game-day. Dropping it means the next boot treats every purse as never
-- assessed, so the first round after a rollback collects once regardless of
-- whether today's levy was already taken. Engine-STOPPED like the up.

BEGIN;

ALTER TABLE actor DROP COLUMN IF EXISTS estate_rate_assessed_at;

COMMIT;
