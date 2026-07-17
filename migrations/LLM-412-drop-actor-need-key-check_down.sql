-- Revert LLM-412 drop-actor-need-key-check: intentional no-op.
--
-- The _up removed the actor_need.key enum CHECK on purpose — the engine is now
-- authoritative on the valid need set. There is no safe reversal:
--   * Re-adding the original enum ('hunger','thirst','tiredness') would instantly
--     fail (or, once validated, reject) the live `cold` need rows and re-break
--     checkpoint durability the moment a storm is active.
--   * Re-adding a wider enum that includes `cold` just reintroduces the exact
--     schema-coupled fragility this migration exists to remove, and would go
--     stale again on the next new need.
-- So the reversal is deliberately empty. Manual-rollback only (the runner never
-- applies _down.sql); if a key-set guard is ever truly wanted again, author it
-- as a new forward migration with the then-current need set.

BEGIN;

-- no-op

COMMIT;
