-- ZBBS-039: Per-state light parameters for illuminated objects.
--
-- Sparse metadata table keyed by asset_state.id — only states that emit light
-- get a row. Consistent with asset_state_tag (ZBBS-025) as the pattern for
-- per-state sparse metadata.
--
-- Seeded from every state currently tagged 'night-active' with a single warm
-- default. Per-asset tuning (color, radius, flicker) happens via plain
-- UPDATE statements after we see the result in-game.

CREATE TABLE asset_state_light (
    state_id INT PRIMARY KEY REFERENCES asset_state(id) ON DELETE CASCADE,
    color VARCHAR(7) NOT NULL,                              -- hex #RRGGBB
    radius INT NOT NULL,                                    -- world pixels (after 2x sprite scale)
    energy DOUBLE PRECISION NOT NULL DEFAULT 1.0,
    offset_x INT NOT NULL DEFAULT 0,                        -- light center offset from sprite origin
    offset_y INT NOT NULL DEFAULT 0,
    flicker_amplitude DOUBLE PRECISION NOT NULL DEFAULT 0,  -- 0 = steady, 0.15 ≈ fire
    flicker_period_ms INT NOT NULL DEFAULT 0                -- cycle time for flicker
);

-- Seed: every night-active state gets a warm amber default. Tune per-asset
-- later via SQL once we see what looks right.
INSERT INTO asset_state_light (state_id, color, radius, energy)
SELECT t.state_id, '#FFAA55', 96, 1.0
FROM asset_state_tag t
WHERE t.tag = 'night-active';

-- Campfires and torches flicker. Other lit objects stay steady.
UPDATE asset_state_light
SET color = '#FF8833', radius = 128, flicker_amplitude = 0.15, flicker_period_ms = 180
WHERE state_id IN (
    SELECT s.id FROM asset_state s
    JOIN asset a ON s.asset_id = a.id
    WHERE a.name = 'Campfire' AND s.state = 'default'
);

UPDATE asset_state_light
SET color = '#FF9944', radius = 72, flicker_amplitude = 0.15, flicker_period_ms = 180
WHERE state_id IN (
    SELECT s.id FROM asset_state s
    JOIN asset a ON s.asset_id = a.id
    WHERE a.name = 'Torch' AND s.state = 'default'
);
