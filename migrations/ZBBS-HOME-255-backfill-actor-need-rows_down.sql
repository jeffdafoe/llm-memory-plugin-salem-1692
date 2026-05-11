-- Down for ZBBS-HOME-255 — intentional no-op.
--
-- The up migration backfilled missing actor_need rows. There's no way
-- to distinguish backfilled rows from rows that legitimately have
-- value=0 (a freshly seeded NPC, an actor whose tiredness has been
-- decremented to 0 by recovery), so any DELETE here would scrub real
-- runtime state along with the backfill effect.
--
-- Re-running the up is safe (ON CONFLICT DO NOTHING) so the down →
-- redo cycle still works for testing other migrations against this
-- one.

SELECT 1;
