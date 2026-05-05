-- ZBBS-121 commit 6 down: restore actor.{hunger,thirst,tiredness}
-- columns and repopulate from actor_need. NOT NULL DEFAULT 0 matches
-- the original ZBBS-084 schema. Backfill via correlated subqueries
-- against actor_need (the post-commit-5 source of truth); actors
-- missing rows fall back to the column default of 0.
--
-- Companion migration ZBBS-121-actor-need-table_down.sql still drops
-- the actor_need table separately; this down restores columns
-- without touching that table, so a partial revert (just this commit)
-- gives operators back column-readable need values without losing
-- the row-store. Reverting both this and the table migration leaves
-- the columns as the only source.

BEGIN;

ALTER TABLE actor ADD COLUMN hunger    SMALLINT NOT NULL DEFAULT 0;
ALTER TABLE actor ADD COLUMN thirst    SMALLINT NOT NULL DEFAULT 0;
ALTER TABLE actor ADD COLUMN tiredness SMALLINT NOT NULL DEFAULT 0;

UPDATE actor a
   SET hunger    = COALESCE((SELECT value FROM actor_need WHERE actor_id = a.id AND key = 'hunger'),    0),
       thirst    = COALESCE((SELECT value FROM actor_need WHERE actor_id = a.id AND key = 'thirst'),    0),
       tiredness = COALESCE((SELECT value FROM actor_need WHERE actor_id = a.id AND key = 'tiredness'), 0);

COMMIT;
