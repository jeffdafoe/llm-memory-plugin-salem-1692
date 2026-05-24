-- ZBBS-HOME-296: NPC long-term lodging (v2) — PR2 lodging rate.
--
-- Adds the operator-set rent setting the PR1 migration deliberately
-- omitted (ZBBS-HOME-296-npc-long-term-lodging-v2_up.sql). Stored weekly
-- (the booking/cadence unit) but billed and quoted per night as
-- weeklyRate/7 — operators keep it divisible by 7 so the per-night charge
-- floors cleanly. Default 28 = 4 coins/night.
--
-- Consumed by the keeper/lodger perception nightly-rate hints, the lodger
-- affordability cue, and the engine-auto rebook sweep
-- (engine/sim/lodger_rebook.go). repo/pg/environment.go already loads the
-- key with a code default of 28, so this row only persists the value so an
-- operator can tune it via the settings table. is_public=FALSE matches every
-- other engine tunable (default_outdoor_scene_radius, scene_quote_ttl, etc.) —
-- the engine loader reads all settings regardless of is_public; the flag only
-- gates the public (unauthenticated) settings API, not admin/operator access.
-- Idempotent.

BEGIN;

INSERT INTO setting (key, value, description, is_public) VALUES
    ('lodging_default_weekly_rate', '28',
     'Operator-set weekly rent for a private room. Billed and quoted per night as rate/7 (keep divisible by 7). 0 or any value below 7 disables the lodging rate surfaces and the engine-auto rebook. Default 28 = 4 coins/night.',
     FALSE)
ON CONFLICT (key) DO NOTHING;

COMMIT;
