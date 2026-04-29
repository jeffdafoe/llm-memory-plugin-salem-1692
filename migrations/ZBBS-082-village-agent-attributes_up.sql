-- ZBBS-082: Village agent needs (hunger, thirst, tiredness) and currency seed.
--
-- Three new SMALLINT columns on village_agent for the simulation needs that
-- will eventually drive want-driven NPC behavior (when hungry, go to the
-- tavern; when tired, go home to sleep; etc.). Each runs 0–24 with higher =
-- more in need. Decay is time-based; reset is consumption-based.
--
-- Currency: village_agent.coins already exists (default 100, set in
-- ZBBS-005). Reset all existing rows to 20 and change the column default to
-- 20 so newly-spawned villagers start at the same baseline.
--
-- New setting rows:
--   - attribute_tick_amount: how much each need grows per simulated hour.
--     Capped at 24 in code (LEAST in the UPDATE).
--   - meal_drop / drink_drop: how much hunger / thirst drops when an NPC
--     pays at a tavern. Default 24 (effectively full reset to 0). Tunable
--     for partial-meal experiments later.
--   - last_attribute_tick_hour: state row, not config. Stores the wall-clock
--     hour-of-day (0–23) of the most recent attribute increment so the
--     per-minute server tick handler can detect hour-boundary crossings
--     idempotently without per-NPC stamps. NULL means "never run yet" —
--     handler treats first run as no-op (records the current hour) so
--     server restart never causes a double-fire.
--
-- Prompt integration deferred — schema + mechanics only. The model is not
-- yet told about these attributes or the pay tool.

BEGIN;

ALTER TABLE village_agent ADD COLUMN hunger    SMALLINT NOT NULL DEFAULT 0;
ALTER TABLE village_agent ADD COLUMN thirst    SMALLINT NOT NULL DEFAULT 0;
ALTER TABLE village_agent ADD COLUMN tiredness SMALLINT NOT NULL DEFAULT 0;

UPDATE village_agent SET coins = 20;
ALTER TABLE village_agent ALTER COLUMN coins SET DEFAULT 20;

INSERT INTO setting (key, value, description) VALUES
    ('attribute_tick_amount', '1',  'Amount added to each NPC need (hunger/thirst/tiredness) per simulated hour. Capped at 24 in code.'),
    ('meal_drop',             '24', 'Amount subtracted from hunger when an NPC pays for food at a tavern. Floored at 0. Default 24 = full reset.'),
    ('drink_drop',            '24', 'Amount subtracted from thirst when an NPC pays for drink at a tavern. Floored at 0. Default 24 = full reset.'),
    ('last_attribute_tick_hour', NULL, 'State row (not config). Wall-clock hour-of-day (0-23) of the most recent attribute tick. NULL = never run.')
ON CONFLICT (key) DO NOTHING;

COMMIT;
