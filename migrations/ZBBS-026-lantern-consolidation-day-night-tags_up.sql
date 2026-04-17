-- ZBBS-026: Collapse Hanging Lantern dark/lit pairs into single assets with
-- day/night states. Tag Campfire + Lantern states with day-active/night-active
-- so the scheduler can flip them at dawn/dusk.
--
-- Scope:
-- * Hanging Lantern 16x32 sheet pair -> "Hanging Lantern (Mini)" with unlit+lit states
-- * Hanging Lantern 32x32 sheet pair -> "Hanging Lantern" with unlit+lit states
-- * Campfire (4 placements) -> keep state names, just add tags
-- * Both new lantern assets -> tag states
--
-- Not in scope (separate passes):
-- * Lamp Post lit/unlit variants (need to eyeball the 48x80 sheet)
-- * Torch unlit state (need to locate the source sprite; torch sheet has lit-only)
-- * Standalone Lantern at (144,32) on 16x16 sheet (one row, need to check for lit variant)

-- ----- Part 1: drop the existing 4 Hanging Lantern asset rows (0 placements, safe) -----

DELETE FROM asset_state
WHERE asset_id IN (SELECT id FROM asset WHERE name IN ('Hanging Lantern (Dark)', 'Hanging Lantern (Lit)'));

DELETE FROM asset
WHERE name IN ('Hanging Lantern (Dark)', 'Hanging Lantern (Lit)');

-- ----- Part 2: recreate as single assets with unlit (default, day) + lit (night) states -----

-- 16x32 pair -> Hanging Lantern (Mini)
INSERT INTO asset (id, name, category, default_state, anchor_x, anchor_y, layer, pack_id, source_file) VALUES
    (gen_random_uuid(), 'Hanging Lantern (Mini)', 'prop', 'unlit', 0.5, 0.85, 'objects', 'mana-seed', 'village accessories 16x32.png');

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'unlit', '/tilesets/mana-seed/village-accessories/village accessories 16x32.png', 0, 32, 16, 32, 1, 0
FROM asset WHERE name = 'Hanging Lantern (Mini)' AND pack_id = 'mana-seed';

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'lit', '/tilesets/mana-seed/village-accessories/village accessories 16x32.png', 16, 32, 16, 32, 1, 0
FROM asset WHERE name = 'Hanging Lantern (Mini)' AND pack_id = 'mana-seed';

-- 32x32 pair -> Hanging Lantern
INSERT INTO asset (id, name, category, default_state, anchor_x, anchor_y, layer, pack_id, source_file) VALUES
    (gen_random_uuid(), 'Hanging Lantern', 'prop', 'unlit', 0.5, 0.85, 'objects', 'mana-seed', 'village accessories 32x32.png');

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'unlit', '/tilesets/mana-seed/village-accessories/village accessories 32x32.png', 0, 64, 32, 32, 1, 0
FROM asset WHERE name = 'Hanging Lantern' AND pack_id = 'mana-seed';

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'lit', '/tilesets/mana-seed/village-accessories/village accessories 32x32.png', 32, 64, 32, 32, 1, 0
FROM asset WHERE name = 'Hanging Lantern' AND pack_id = 'mana-seed';

-- ----- Part 3: tag day/night on lantern states + existing Campfire states -----

-- Hanging Lantern (Mini) + Hanging Lantern: unlit = day-active, lit = night-active
INSERT INTO asset_state_tag (state_id, tag)
SELECT s.id, 'day-active'
FROM asset_state s JOIN asset a ON a.id = s.asset_id
WHERE a.name IN ('Hanging Lantern', 'Hanging Lantern (Mini)') AND s.state = 'unlit';

INSERT INTO asset_state_tag (state_id, tag)
SELECT s.id, 'night-active'
FROM asset_state s JOIN asset a ON a.id = s.asset_id
WHERE a.name IN ('Hanging Lantern', 'Hanging Lantern (Mini)') AND s.state = 'lit';

-- Campfire: default (lit, animated) is night-active; unlit (smoking, animated) is day-active
INSERT INTO asset_state_tag (state_id, tag)
SELECT s.id, 'night-active'
FROM asset_state s JOIN asset a ON a.id = s.asset_id
WHERE a.name = 'Campfire' AND s.state = 'default';

INSERT INTO asset_state_tag (state_id, tag)
SELECT s.id, 'day-active'
FROM asset_state s JOIN asset a ON a.id = s.asset_id
WHERE a.name = 'Campfire' AND s.state = 'unlit';
