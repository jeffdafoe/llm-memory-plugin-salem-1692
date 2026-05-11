-- ZBBS-HOME-256 — heal actors with NULL inside_room_id while inside.
--
-- An actor with `inside = true` and `inside_structure_id` set MUST
-- have `inside_room_id` set too — every co-located cascade and most
-- room-scoped queries assume the pairing. The combination
-- `(inside=true, inside_structure_id=<x>, inside_room_id=NULL)` is
-- invalid state.
--
-- It happened anyway: setNPCInside (engine/npc_behaviors.go) returned
-- early when `inside` and `inside_structure_id` already matched the
-- desired values, without checking whether `inside_room_id` matched.
-- An actor that landed in the broken state (post-ZBBS-149 backfill
-- gap, mid-flight migration race, etc.) stayed in it indefinitely —
-- subsequent setNPCInside calls saw "already inside the right
-- structure" and skipped the UPDATE that would have written the
-- common-room id.
--
-- John Ellis was in this state today: inside=true, inside_structure_id
-- = Tavern, inside_room_id = NULL. The arrival cascade query at
-- agent_tick.go ~1080 requires receiver.inside_room_id = trigger
-- actor's room — `NULL = 16` evaluates NULL → falsy → John filtered
-- out, no event-tick fired for him on PC arrival, no greeting.
--
-- Companion engine fixes:
--   * agent_tick.go: cascade query now COALESCEs receiver
--     inside_room_id to common before comparing, so a future broken
--     row still gets reached.
--   * npc_behaviors.go setNPCInside: early-return now also checks
--     inside_room_id parity, so subsequent inside-state writes heal
--     the column instead of skipping it.
--
-- This migration heals existing rows once. Idempotent — re-runs
-- update zero rows since the predicate filters to broken rows only.

BEGIN;

UPDATE actor a
   SET inside_room_id = (
         SELECT sr.id FROM structure_room sr
          WHERE sr.structure_id = a.inside_structure_id
            AND sr.kind = 'common'
          LIMIT 1
       )
 WHERE a.inside = true
   AND a.inside_structure_id IS NOT NULL
   AND a.inside_room_id IS NULL;

COMMIT;
