-- LLM-373: give in-flight travelers a durable day-plan.
--
-- The transient-visitor framework (engine/sim/visitor.go) grew a purposeful
-- day: a traveler arrives in daylight, walks a circuit of the open businesses
-- trading and passing news, is drawn to the tavern of an evening, books a room
-- from its pack through the real lodging flow, sleeps, and leaves at daybreak.
-- Three pieces of that state are mutable across the visit and must survive the
-- constant deploys so a mid-stay restart resumes the traveler exactly where it
-- was rather than restarting its rounds or losing the room it paid for:
--
--   * itinerary  — which businesses it has already made its round at, the shop
--                  it is currently walking to / lingering at, and its dwell
--                  timer (VisitorState.VisitedBusinesses / RoundTarget /
--                  DwellUntil).
--   * pack + purse — the wares and coins it spawned carrying (Actor.Inventory /
--                  Actor.Coins). This is both its lodging payment (barter, per
--                  LLM-353) and its trade stock; without it a restart would
--                  strand a mid-stay traveler unable to pay for a room it booked.
--   * booked room — the RoomAccess grant it holds once checked in. Visitors are
--                  firewalled out of the 11-tier actor aggregate that writes
--                  room_access (ActorsRepo.SaveSnapshot skips VisitorState != nil),
--                  so a traveler's grant is NOT in the room_access table; it rides
--                  here so the room stays booked across a restart.
--
-- This is the nested/list state the LLM-369 tier comment reserved for LLM-373
-- ("the day-plan pack + itinerary land as jsonb then, exactly the way
-- labor_contract carries reward_items"). One jsonb column, not a spray of typed
-- columns, because the shape is a small evolving document and none of these
-- fields are reconcile-critical (the boot reconcile keys on expires_at + phase,
-- already typed columns). The engine (de)serializes it in
-- engine/sim/repo/pg/visitors.go alongside the generation-marker UPSERT / DELETE.
--
-- NOT NULL DEFAULT '{}' — an empty object is the well-defined "no plan state yet"
-- value (a freshly-spawned traveler before its first checkpoint, or a visitor row
-- written by an LLM-369/371/372 engine that predates this column). The engine
-- treats a missing key as its zero value, so the default backfills cleanly.
--
-- Engine-checkpointed standalone aggregate → deploy stop -> migrate -> start.
-- IF NOT EXISTS so a re-run (or a future re-baseline that folds this into
-- schema.sql, then replays) is a clean no-op under ON_ERROR_STOP=1.
BEGIN;

ALTER TABLE public.visitor
    ADD COLUMN IF NOT EXISTS plan jsonb NOT NULL DEFAULT '{}'::jsonb;

COMMIT;
