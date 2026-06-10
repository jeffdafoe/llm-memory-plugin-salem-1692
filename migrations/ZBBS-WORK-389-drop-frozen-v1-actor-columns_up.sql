-- ZBBS-WORK-389: drop the frozen v1-only actor bookkeeping columns.
--
-- v2 reads/writes only the actor columns the engine actually tracks
-- (engine/sim/repo/pg/actors.go: loadAllSQLA + upsertSQLA). The columns
-- dropped here are in neither list, so on every existing row they have
-- been frozen at their last v1-era write since v2 go-live — values that
-- can only mislead anyone eyeballing the table (last_pc_seen_at reads
-- like PC liveness but stopped updating when v1 died). Verified
-- 2026-06-10 against engine HEAD 176d99d: zero Go references to any of
-- them; the Godot client talks engine DTOs, never SQL; memory-api is a
-- separate database. Full audit in
-- shared/tasks/done/zbbs-work-389-drop-frozen-v1-actor-columns.
--
-- Per column, where the live replacement lives:
--   inside                      derived: inside_structure_id IS NOT NULL
--   llm_memory_api_key          vestigial since ZBBS-084 (never read by v1 OR v2)
--   home_x / home_y             v1 spawn point; v2 has no consumer
--   lateness_window_minutes     global shift_lateness_window_minutes setting
--                               (ZBBS-HOME-309 moved staggering off the per-NPC column)
--   last_shift_tick_at          zero consumers anywhere
--   agent_override_until        deliberately not ported (SEAM E, mail 9cf4bcf0):
--                               v2 keys break/sleep exclusion on BreakUntil/SleepingUntil
--   last_pc_input_at            v2 tracks PC liveness in-memory
--   last_pc_seen_at             same
--   last_tiredness_recovery_at  v2 keeps the recovery cursor in-memory
--                               (tiredness_recovery.go: transient cadence state)
--   visitor_* (4 columns)       v2 filters visitor actors out of persistence
--                               entirely; rows + columns are pure v1 residue
--
-- Visitor ROWS are deleted before their identifying columns go away —
-- this is the "cutover-prep migration will delete visitor rows" cleanup
-- that engine/sim/repo/pg/actors.go's header deferred and that never got
-- a vehicle until now. Every FK referencing actor(id) is ON DELETE
-- CASCADE or SET NULL (schema.sql), so dependents clean up with the row.
-- Expected to delete 0 rows on a healthy DB; harmless if so.
--
-- actor_lateness_window_minutes_check and idx_actor_visitor_expires drop
-- automatically with their columns.

BEGIN;

-- Predicate spans the whole visitor cluster, not just archetype: v1 is
-- deleted so "archetype was always set" is unprovable, and a partial
-- v1 write surviving past the drop would be an unidentifiable orphan.
DELETE FROM public.actor
 WHERE visitor_archetype IS NOT NULL
    OR visitor_expires_at IS NOT NULL
    OR visitor_origin IS NOT NULL
    OR visitor_disposition IS NOT NULL;

ALTER TABLE public.actor
    DROP COLUMN inside,
    DROP COLUMN llm_memory_api_key,
    DROP COLUMN home_x,
    DROP COLUMN home_y,
    DROP COLUMN lateness_window_minutes,
    DROP COLUMN last_shift_tick_at,
    DROP COLUMN agent_override_until,
    DROP COLUMN last_pc_input_at,
    DROP COLUMN last_pc_seen_at,
    DROP COLUMN last_tiredness_recovery_at,
    DROP COLUMN visitor_expires_at,
    DROP COLUMN visitor_archetype,
    DROP COLUMN visitor_origin,
    DROP COLUMN visitor_disposition;

COMMIT;
