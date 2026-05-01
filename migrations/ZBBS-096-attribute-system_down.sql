-- ZBBS-095 down: drop the attribute system tables.
--
-- actor_attribute first (FK depends on attribute_definition), then the
-- definition table itself. CASCADE on actor_attribute is unnecessary
-- because the table is being dropped wholesale; explicit DROP TABLE is
-- enough. The actor.behavior column and npc_behavior table were left
-- intact by the up migration, so no restoration needed here.

BEGIN;

DROP INDEX IF EXISTS idx_actor_attribute_slug;
DROP TABLE IF EXISTS actor_attribute;
DROP TABLE IF EXISTS attribute_definition;

COMMIT;
