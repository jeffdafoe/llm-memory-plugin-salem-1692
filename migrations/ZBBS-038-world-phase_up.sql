-- ZBBS-038: World day/night phase state + tunable dawn/dusk/timezone settings.
--
-- Adds a singleton world_phase row that the engine ticker maintains. On each
-- dawn/dusk crossing the ticker bulk-updates village_object.current_state via
-- asset_state_tag ('day-active' / 'night-active') and broadcasts a per-row
-- object_state_changed event.
--
-- Dawn/dusk times and the reference timezone are tunable via the setting table.

CREATE TABLE world_phase (
    id INT PRIMARY KEY DEFAULT 1,
    phase VARCHAR(20) NOT NULL CHECK (phase IN ('day', 'night')),
    last_transition_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT world_phase_singleton CHECK (id = 1)
);

INSERT INTO world_phase (id, phase) VALUES (1, 'day');

INSERT INTO setting (key, value, description, is_public) VALUES
    ('world_dawn_time', '07:00', 'Time of day (HH:MM in world_timezone) when the world transitions to day phase', false),
    ('world_dusk_time', '19:00', 'Time of day (HH:MM in world_timezone) when the world transitions to night phase', false),
    ('world_timezone', 'America/New_York', 'IANA timezone name for the world clock (e.g. America/New_York)', false);
