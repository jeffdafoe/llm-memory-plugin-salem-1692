-- Reverse of ZBBS-149-actor-subspace_up.sql.
--
-- Drops actor.inside_subspace_id, then the access + subspace tables,
-- then the enum. Engine code that references these (subspace.go,
-- setActorInside helper, executeDeliverOrder lodger-room assignment,
-- /pc/move-subspace handler, perception subspace filter) must be
-- rolled back alongside this migration.

BEGIN;

ALTER TABLE actor DROP COLUMN IF EXISTS inside_subspace_id;

DROP TABLE IF EXISTS subspace_access;
DROP TABLE IF EXISTS structure_subspace;

DROP TYPE IF EXISTS subspace_kind;

COMMIT;
