-- ZBBS-119: Chronicler buffered dispatch — settings for the new dispatcher.
--
-- Today every village event (NPC arrival, shift boundary, needs change)
-- fires its own immediate chronicler cascade. When several events land in
-- the same window — three NPCs arriving at their workplaces within
-- seconds of each other at midday, for example — the engine spawns
-- parallel chronicler scenes. Each chronicler tries to attend NPCs the
-- other cascades have already dispatched, and the in-flight gate at
-- agent_tick.go drops the redundant attempts. Result: short, semi-empty
-- scenes; wasted chronicler turn budget; cross-cascade race.
--
-- The buffered dispatcher serializes all chronicler fires through one
-- per-world slot, with a configurable buffer window for routine events
-- (arrivals, shift boundaries, atmosphere) and an early-flush escape
-- hatch for high-priority events (PC speech, PC arrival, admin
-- attend-now). See shared/tasks/pending/salem-chronicler-buffered-dispatch
-- for the full design.
--
-- This migration adds the two settings the dispatcher reads. The
-- behavioral change ships behind chronicler_buffered_dispatch=false
-- so the new path is dark until an admin flips the flag.

BEGIN;

INSERT INTO setting (key, value, description) VALUES
    ('chronicler_buffer_window_seconds', '60', 'Buffered chronicler dispatch window, in seconds (5-600). Routine events (arrival, shift_boundary, atmosphere, needs_resolved) accumulate in the dispatcher queue and flush as a single consolidated chronicler fire when the window elapses. Default 60 aligns with the per-minute scheduler beat. Higher = cheaper + more coalesced narrative; lower = more responsive + more fires. Only consulted when chronicler_buffered_dispatch=true.'),
    ('chronicler_buffered_dispatch',     'false', 'Feature flag for the buffered chronicler dispatcher. When false, the legacy immediate-fire path runs unchanged (every cascade origin fires its own chronicler scene). When true, routine events are buffered through the dispatcher and high-priority events (PC speech, PC arrival, admin attend-now) early-flush the buffer. All fires serialize through one in-flight slot per world. Set to true once the new path has been observed clean for a session.')
ON CONFLICT (key) DO NOTHING;

COMMIT;
