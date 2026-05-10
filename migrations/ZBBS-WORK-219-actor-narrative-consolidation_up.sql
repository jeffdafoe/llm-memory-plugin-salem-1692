-- ZBBS-WORK-219 — actor_narrative_state consolidation tracking.
--
-- Phase 4 of the engine-side continuity layer: per-actor reflection.
-- Phase 3 (WORK-218) compresses per-pair salient_facts into
-- actor_relationship.summary_text. Phase 4 closes the cross-peer cut:
-- a daily pass that distills the actor's recent agent_action_log rows
-- + their current relationship summaries into actor_narrative_state.
-- evolving_summary — the "where you are in your own story right now"
-- block that already surfaces in the "Who you are:" perception
-- section (alongside seed_text) from Phase 1A.
--
-- The marker column mirrors WORK-218's pattern on actor_relationship.
-- updated_at would conflict with manual edits via UPDATE (Jeff
-- revising Hannah's seed_text) and with any future event-driven
-- writes to evolving_summary; the dedicated column avoids that
-- coupling.

BEGIN;

ALTER TABLE actor_narrative_state
    ADD COLUMN last_consolidated_at TIMESTAMPTZ;

-- Index for the sweep's selection ordering. No partial predicate —
-- every shared-VA-backed actor is a candidate; the gate happens
-- in the SQL WHERE clause via JOIN to actor.llm_memory_agent.
CREATE INDEX idx_actor_narrative_state_consolidation
    ON actor_narrative_state (last_consolidated_at NULLS FIRST);

COMMIT;
