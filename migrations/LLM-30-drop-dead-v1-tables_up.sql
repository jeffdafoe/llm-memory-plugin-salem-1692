-- LLM-30: drop the dead v1-only + abandoned-v2 tables from the zbbs DB.
--
-- The v1 Go monolith was deleted (PR #382 / WORK-383), so every table only v1
-- touched is orphaned. The sweep of migrations/schema.sql against the v2 engine
-- (engine/sim, including repo/pg) plus a prod cross-check found ten dead tables.
-- None is read or written anywhere in engine/sim.
--
--   v1 chronicler / atmosphere / gossip / crier:
--     world_environment, world_events, village_event, village_concern,
--     village_gossip, town_crier_announcement
--       (v2's town crier reads the village_object noticeboard, not this table)
--   v1 foraging:
--     gatherable_node
--   v2 economy foundation (HOME-241..247), abandoned by the HOME-247..254
--   consolidation that moved buy/restock/delivery state in-process:
--     actor_buy_state, actor_restock_in_progress, actor_delivery_in_progress
--
-- Confirmed FK-clean: nothing references any of these tables (no inbound FKs,
-- no dependent views), so DROP needs no CASCADE; each table's owned sequence +
-- indexes drop automatically with it. The now-unused enum types
-- (chronicler_phase, concern_source_kind, concern_target_kind, event_scope) are
-- left in place -- out of scope here, harmless.
--
-- One-way door: the dropped rows are v1 game history; recoverable only from the
-- VPS nightly backup. Jeff signed off on the full ten-table list (2026-06-19).

BEGIN;

DROP TABLE IF EXISTS public.world_environment;
DROP TABLE IF EXISTS public.world_events;
DROP TABLE IF EXISTS public.village_event;
DROP TABLE IF EXISTS public.village_concern;
DROP TABLE IF EXISTS public.village_gossip;
DROP TABLE IF EXISTS public.town_crier_announcement;
DROP TABLE IF EXISTS public.gatherable_node;
DROP TABLE IF EXISTS public.actor_buy_state;
DROP TABLE IF EXISTS public.actor_restock_in_progress;
DROP TABLE IF EXISTS public.actor_delivery_in_progress;

COMMIT;
