-- ZBBS-123: Movement fatigue — config setting.
--
-- Introduces movement_fatigue_per_tile_x100, the per-tile cost in
-- 1/100ths of a tiredness point. On every successful agent or PC
-- move-commit (move_to / chore / pc/move), the engine computes
-- Euclidean tile distance from the actor's current position to the
-- walk target and bumps actor_need.tiredness by
-- floor(tiles * cfg / 100), capped at the 0..24 ceiling.
--
-- 1/100ths granularity lets short walks floor to zero — popping next
-- door is free — without needing a sub-integer storage column. The
-- fractional remainder is intentionally lost; in practice the noise
-- is below the threshold the perception surfaces.
--
-- Default 12: sized so that ~4-6 round-trips between the village
-- center and a map-edge structure (orchard, distant well) wears a
-- rested villager into red-tier weariness, while same-block hops
-- (tavern → home, store → blacksmith) cost nothing. Tune live by
-- UPDATE setting; setting the value to '0' disables fatigue
-- accrual entirely without code changes.
--
-- is_public=false because the value is engine-internal calibration
-- and shouldn't leak through public settings endpoints.
--
-- Idempotent via ON CONFLICT — a re-run with a manually-tuned value
-- in place won't reset the operator's calibration.

BEGIN;

INSERT INTO setting (key, value, description, is_public) VALUES
    ('movement_fatigue_per_tile_x100', '12',
     'Tiredness accrued per tile walked, in 1/100ths. Set to 0 to disable.',
     false)
ON CONFLICT (key) DO NOTHING;

COMMIT;
