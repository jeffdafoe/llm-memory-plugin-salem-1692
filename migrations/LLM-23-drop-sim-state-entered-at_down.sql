-- LLM-23 rollback: re-add actor.sim_state_entered_at with its original
-- baseline definition (TIMESTAMPTZ NOT NULL DEFAULT now()). Manual rollback
-- only -- the deploy runner applies *_up.sql, not *_down.sql. Existing rows
-- are backfilled with now() via the default; the original per-actor seed
-- timestamps are not restored (they were frozen and behaviorally meaningless).

BEGIN;

ALTER TABLE actor ADD COLUMN IF NOT EXISTS sim_state_entered_at timestamp with time zone NOT NULL DEFAULT now();

COMMIT;
