-- ZBBS-110: scheduled self-tick mechanism for NPCs.
--
-- NPC ticks are reactive-only — they fire from cascade origins (PC speech,
-- NPC arrival, heard-speech, chronicler dispatch, shift boundary). There
-- is no facility for an NPC to "tick myself again in 30 seconds" so the
-- conversation can breathe before they commit a follow-up action.
--
-- This migration adds a per-actor scheduled-tick slot. The server tick
-- drains entries whose fire time has passed and force-triggers the NPC's
-- harness. Single slot per actor — scheduling a sooner tick wins, a later
-- one is ignored.
--
-- First user: return-to-work nudge. When perception detects "on shift,
-- away from work, no pressing need," the harness end schedules a follow-up
-- ~30-60s later. The next firing rebuilds perception (nudge still active
-- if NPC didn't move) and gives the LLM another chance to commit move_to
-- work. Reusable for any future "I want a beat before acting" pattern.

BEGIN;

ALTER TABLE actor
    ADD COLUMN next_self_tick_at TIMESTAMP NULL,
    ADD COLUMN next_self_tick_reason TEXT NULL;

CREATE INDEX idx_actor_next_self_tick_at
    ON actor(next_self_tick_at)
    WHERE next_self_tick_at IS NOT NULL;

COMMIT;
