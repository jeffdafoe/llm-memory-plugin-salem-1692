-- ZBBS-025: Consolidate Notice Boards into one asset with multiple states;
-- introduce asset_state_tag table for declarative scheduling.
--
-- Why: ZBBS-024 brought in 5 notice-board variants as separate assets. That's the
-- wrong shape — we want one "Notice Board" you place and a scheduler flips its
-- state between variants on a daily rotation. The schema already supports multi-state
-- assets via asset_state; we just need to model these as states, not assets.
--
-- The state_tag table lets the scheduler query by tag ('rotatable', 'night-active',
-- 'day-active') without hardcoding asset names.

-- Wipe existing notice board rows. No placements exist yet; safe to DELETE.
DELETE FROM asset_state
WHERE asset_id IN (SELECT id FROM asset WHERE name IN ('Notice Board', 'Notice Board 1', 'Notice Board 2', 'Notice Board 3', 'Notice Board 4', 'Notice Board 5'));

DELETE FROM asset
WHERE name IN ('Notice Board', 'Notice Board 1', 'Notice Board 2', 'Notice Board 3', 'Notice Board 4', 'Notice Board 5');

-- Tag table: declarative scheduling hook. state_id references asset_state.id (INT PK).
CREATE TABLE IF NOT EXISTS asset_state_tag (
    state_id INT NOT NULL REFERENCES asset_state(id) ON DELETE CASCADE,
    tag VARCHAR(50) NOT NULL,
    PRIMARY KEY (state_id, tag)
);
CREATE INDEX IF NOT EXISTS idx_asset_state_tag_tag ON asset_state_tag(tag);

-- Single Notice Board asset with 5 variants. default_state picks the first.
INSERT INTO asset (id, name, category, default_state, anchor_x, anchor_y, layer, pack_id, source_file) VALUES
    (gen_random_uuid(), 'Notice Board', 'prop', 'variant-1', 0.5, 0.85, 'objects', 'mana-seed', 'village notice boards 48x64.png');

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'variant-1', '/tilesets/mana-seed/village-accessories/village notice boards 48x64.png', 192, 0, 48, 64, 1, 0
FROM asset WHERE name = 'Notice Board' AND pack_id = 'mana-seed';

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'variant-2', '/tilesets/mana-seed/village-accessories/village notice boards 48x64.png', 240, 0, 48, 64, 1, 0
FROM asset WHERE name = 'Notice Board' AND pack_id = 'mana-seed';

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'variant-3', '/tilesets/mana-seed/village-accessories/village notice boards 48x64.png', 288, 0, 48, 64, 1, 0
FROM asset WHERE name = 'Notice Board' AND pack_id = 'mana-seed';

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'variant-4', '/tilesets/mana-seed/village-accessories/village notice boards 48x64.png', 336, 0, 48, 64, 1, 0
FROM asset WHERE name = 'Notice Board' AND pack_id = 'mana-seed';

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'variant-5', '/tilesets/mana-seed/village-accessories/village notice boards 48x64.png', 384, 0, 48, 64, 1, 0
FROM asset WHERE name = 'Notice Board' AND pack_id = 'mana-seed';

-- Tag all five variants as rotatable so the town-crier scheduler can pick them up later.
INSERT INTO asset_state_tag (state_id, tag)
SELECT s.id, 'rotatable'
FROM asset_state s
JOIN asset a ON a.id = s.asset_id
WHERE a.name = 'Notice Board' AND a.pack_id = 'mana-seed';
