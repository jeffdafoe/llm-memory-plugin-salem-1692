-- ZBBS-121 down: drop actor_need table.
--
-- Does not restore values to the legacy actor columns — those still
-- exist after this commit (they're not dropped until the
-- column-removal commit later in the refactor). If you're reverting
-- this commit, the legacy columns are still authoritative and have
-- the current values; the actor_need rows being deleted are stale
-- only for the period between this revert and any subsequent
-- consumption that wrote to both.

BEGIN;

DROP TABLE IF EXISTS actor_need;

COMMIT;
