-- ZBBS-HOME-440: drop the v1 self-tick scheduler columns.
--
-- next_self_tick_at / next_self_tick_reason were the v1 engine's
-- self-tick scheduler cursor ("when do I wake this actor next, and
-- why"). Nothing in v2 writes them — v2 liveness comes from the warrant
-- system (engine/sim/reactor.go) plus the cascade idle-backstop sweep
-- (engine/sim/cascade/idle_backstop.go), both of which keep their state
-- in memory. The columns only round-tripped through LoadAll/SaveSnapshot
-- (engine/sim/repo/pg/actors.go), so every row has been frozen at its
-- last v1-era write since v2 go-live. The stale values actively mislead:
-- during 2026-06-12 live debugging, Josiah Thorne's month-old
-- "idle-backstop" cursor in the umbilical agent view sent an
-- investigation down the wrong path. The same ticket removes the Go
-- struct fields, the pg round-trip, and the umbilical surface; this
-- migration drops the storage. WORK-389 (the frozen-v1-column sweep)
-- left these two alone only because the v2 code still round-tripped
-- them at the time.
--
-- idx_actor_next_self_tick_at (partial btree on the v1 sweep predicate)
-- drops automatically with its column.

BEGIN;

ALTER TABLE public.actor
    DROP COLUMN next_self_tick_at,
    DROP COLUMN next_self_tick_reason;

COMMIT;
