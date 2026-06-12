-- ZBBS-HOME-449: checkpoint in-flight walks so a restart doesn't strand
-- mid-walk actors.
--
-- MoveIntent has always been ephemeral ("regenerates on first reactor
-- activity" — engine/sim/repo/pg/actors.go LoadAll header), but for an
-- off-shift actor there IS no first reactor activity: no duty steer, no
-- nearby events, and the noop-skip gate eats the idle-backstop ticks.
-- Live case 2026-06-12: Ezekiel Crane's Inn-bound walk was killed by the
-- 20:09:40 deploy restart, leaving him standing at the road midpoint
-- (107,135) indefinitely.
--
-- The column carries the walk's DESTINATION only (kind + target id/tile
-- + knock flag) as jsonb, derived from the live MoveIntent at every
-- checkpoint write — NULL when the actor isn't walking. At boot the
-- engine re-dispatches it through the normal MoveActor command (path is
-- re-planned from the checkpointed tile; arrival fires the normal
-- arrival warrant, so the actor finishes the plan it was executing).
--
-- Safe to apply with the engine running: the old binary's upsert names
-- neither column, and ON CONFLICT touches only listed columns.

BEGIN;

ALTER TABLE public.actor
    ADD COLUMN move_destination jsonb;

COMMIT;
