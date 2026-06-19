-- LLM-23 rollback: re-add actor.sim_state_entered_at and restore its original
-- baseline definition (TIMESTAMPTZ NOT NULL DEFAULT now()). Manual rollback
-- only -- the deploy runner applies *_up.sql, not *_down.sql. Existing rows are
-- backfilled with now(); the original per-actor seed timestamps are not
-- restored (they were frozen and behaviorally meaningless).
--
-- Done in three steps rather than a single ADD ... NOT NULL DEFAULT now() so the
-- end state is the baseline definition even on a drifted target that already has
-- the column nullable or without the default.

BEGIN;

ALTER TABLE actor
    ADD COLUMN IF NOT EXISTS sim_state_entered_at timestamp with time zone;

UPDATE actor
   SET sim_state_entered_at = now()
 WHERE sim_state_entered_at IS NULL;

ALTER TABLE actor
    ALTER COLUMN sim_state_entered_at SET DEFAULT now(),
    ALTER COLUMN sim_state_entered_at SET NOT NULL;

COMMIT;
