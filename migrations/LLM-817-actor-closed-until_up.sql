-- LLM-817: actor.closed_until — a keeper's closed-early mark (sim.Actor.ClosedUntil).
--
-- WHY. A keeper who goes home late in its shift has closed for the day. The mark
-- stops the shift-duty warrant and the return-to-post steer from sending it back
-- until its shift ends. It is checkpointed so an engine restart in the evening
-- does not bring the nagging back.
--
-- ENGINE-OWNED TABLE (actor). Apply with the engine STOPPED (stop -> migrate ->
-- start, the standard deploy order): the old binary's checkpoint does not write
-- closed_until and the new binary's does.

BEGIN;

ALTER TABLE actor ADD COLUMN IF NOT EXISTS closed_until timestamp with time zone;

COMMIT;
