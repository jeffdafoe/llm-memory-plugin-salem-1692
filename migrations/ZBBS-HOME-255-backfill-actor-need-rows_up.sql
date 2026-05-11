-- ZBBS-HOME-255 — backfill missing actor_need rows for NPCs.
--
-- Two earlier code paths created NPC actor rows without seeding the
-- accompanying actor_need rows that the recovery sweep + needs tick
-- assume exist:
--
--   1. ZBBS-WORK-204-keeper-seed: created Hannah Boggs via raw INSERT,
--      no seedNeedRowsIfMissing call. The migration is already shipped
--      and applied, so this backfill closes the gap on her row.
--
--   2. engine/visitor.go: the visitor spawn path INSERTs an actor row
--      with llm_memory_agent set to a shared VA (salem-visitor) but
--      bypasses npcs.go's seedNeedRowsIfMissing. Every visitor spawned
--      pre-fix (Caleb Wendell, Nathaniel Pratt today, plus any earlier
--      now-despawned visitors that left the world) is missing their
--      hunger / thirst / tiredness rows. Companion engine fix in
--      visitor.go wraps the INSERT + seed in a tx going forward.
--
-- The cascade effect that motivated the urgency: the tiredness
-- recovery sweep at engine/tiredness_recovery_sweep.go runs every
-- minute over all actors with break_until or sleeping_until set. When
-- it issued UPDATE actor_need on Hannah's missing tiredness row, the
-- UPDATE affected 0 rows, the sweep returned an error, and the entire
-- sweep transaction rolled back. No actor recovered tiredness that
-- minute — including the dedicated-VA NPCs whose rows were intact.
-- Hannah's missing row poisoned the whole batch every minute.
-- Companion engine fix in tiredness_recovery_sweep.go skips-with-log
-- on RowsAffected=0 so a future seeding regression doesn't reopen
-- this hole.
--
-- Seeds 0 (matches seedNeedRowsIfMissing's default). ON CONFLICT DO
-- NOTHING means re-running is safe and existing rows are untouched.

BEGIN;

INSERT INTO actor_need (actor_id, key, value)
SELECT a.id, n.key, 0
  FROM actor a
  CROSS JOIN (VALUES ('hunger'), ('thirst'), ('tiredness')) AS n(key)
 WHERE a.llm_memory_agent IS NOT NULL
ON CONFLICT (actor_id, key) DO NOTHING;

COMMIT;
