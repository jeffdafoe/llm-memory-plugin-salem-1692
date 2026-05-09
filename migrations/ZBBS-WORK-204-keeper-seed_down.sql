-- ZBBS-WORK-204 (commit 1 of 2) down — restore UNIQUE constraint and
-- delete the seeded keeper.
--
-- Restoring the UNIQUE constraint is best-effort: it only succeeds if
-- no two actors currently share the same llm_memory_agent value. If
-- there are concurrent visitors (max_concurrent > 1) or multiple
-- salem-vendor-backed actors at the time of rollback, this migration
-- will fail. Operator must reduce concurrent VA-sharing actors first.

BEGIN;

DELETE FROM actor_inventory
 WHERE actor_id IN (SELECT id FROM actor WHERE display_name = 'Hannah Boggs');

DELETE FROM actor WHERE display_name = 'Hannah Boggs';

DELETE FROM setting WHERE key = 'lodging_default_weekly_rate';

DROP INDEX IF EXISTS idx_actor_llm_memory_agent;
ALTER TABLE actor ADD CONSTRAINT actor_llm_memory_agent_key UNIQUE (llm_memory_agent);

COMMIT;
