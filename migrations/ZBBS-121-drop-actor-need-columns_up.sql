-- ZBBS-121 commit 6: drop the legacy actor.{hunger,thirst,tiredness}
-- columns. The actor_need table (created in
-- ZBBS-121-actor-need-table_up.sql) is now the canonical source for
-- need values; commits 1-5 of the refactor migrated all read and
-- write paths off the columns. After this migration the columns are
-- gone and only actor_need carries the values.
--
-- Pre-deploy assumption: the engine on this host is running at
-- commit a345b84 (commit 5) or later. Earlier engines still
-- referenced the columns in chronicler distress / consumption /
-- hourly tick paths; running this migration against an older engine
-- would break those paths immediately.

BEGIN;

ALTER TABLE actor DROP COLUMN hunger;
ALTER TABLE actor DROP COLUMN thirst;
ALTER TABLE actor DROP COLUMN tiredness;

COMMIT;
