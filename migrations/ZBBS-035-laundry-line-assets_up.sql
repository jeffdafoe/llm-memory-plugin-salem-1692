-- ZBBS-035: Catalog the 4 laundry line sheets as multi-state assets.
-- Each sheet shows the same clothesline with different clothes hung. (0,0) is
-- the empty line; every other cell is a variant. Tag all states 'rotatable' so
-- the scheduler can cycle them daily.

-- ========== Laundry (Post, Straight) — 8 cols x 2 rows = 16 states ==========

INSERT INTO asset (id, name, category, default_state, anchor_x, anchor_y, layer, pack_id, source_file) VALUES
    (gen_random_uuid(), 'Laundry (Post, Straight)', 'prop', 'empty', 0.5, 0.5, 'objects', 'mana-seed', 'village laundry, post straight 64x48.png');

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT
    (SELECT id FROM asset WHERE name = 'Laundry (Post, Straight)' AND pack_id = 'mana-seed'),
    CASE WHEN r = 0 AND c = 0 THEN 'empty' ELSE 'variant-' || (r * 8 + c) END,
    '/tilesets/mana-seed/village-accessories/village laundry, post straight 64x48.png',
    c * 64, r * 48, 64, 48, 1, 0
FROM generate_series(0, 1) r CROSS JOIN generate_series(0, 7) c;

-- ========== Laundry (Alley, Straight) — 8 cols x 3 rows = 24 states ==========

INSERT INTO asset (id, name, category, default_state, anchor_x, anchor_y, layer, pack_id, source_file) VALUES
    (gen_random_uuid(), 'Laundry (Alley, Straight)', 'prop', 'empty', 0.5, 0.5, 'objects', 'mana-seed', 'village laundry, alley straight 64x48.png');

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT
    (SELECT id FROM asset WHERE name = 'Laundry (Alley, Straight)' AND pack_id = 'mana-seed'),
    CASE WHEN r = 0 AND c = 0 THEN 'empty' ELSE 'variant-' || (r * 8 + c) END,
    '/tilesets/mana-seed/village-accessories/village laundry, alley straight 64x48.png',
    c * 64, r * 48, 64, 48, 1, 0
FROM generate_series(0, 2) r CROSS JOIN generate_series(0, 7) c;

-- ========== Laundry (Alley, Angle) — 8 cols x 2 rows = 16 states ==========

INSERT INTO asset (id, name, category, default_state, anchor_x, anchor_y, layer, pack_id, source_file) VALUES
    (gen_random_uuid(), 'Laundry (Alley, Angle)', 'prop', 'empty', 0.5, 0.5, 'objects', 'mana-seed', 'village laundry, alley angle 48x64.png');

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT
    (SELECT id FROM asset WHERE name = 'Laundry (Alley, Angle)' AND pack_id = 'mana-seed'),
    CASE WHEN r = 0 AND c = 0 THEN 'empty' ELSE 'variant-' || (r * 8 + c) END,
    '/tilesets/mana-seed/village-accessories/village laundry, alley angle 48x64.png',
    c * 48, r * 64, 48, 64, 1, 0
FROM generate_series(0, 1) r CROSS JOIN generate_series(0, 7) c;

-- ========== Laundry (Post, Angle) — 8 cols x 1 row; last cell is a duplicate of col 0, skip it ==========

INSERT INTO asset (id, name, category, default_state, anchor_x, anchor_y, layer, pack_id, source_file) VALUES
    (gen_random_uuid(), 'Laundry (Post, Angle)', 'prop', 'empty', 0.5, 0.5, 'objects', 'mana-seed', 'village laundry, post angle 48x64.png');

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT
    (SELECT id FROM asset WHERE name = 'Laundry (Post, Angle)' AND pack_id = 'mana-seed'),
    CASE WHEN c = 0 THEN 'empty' ELSE 'variant-' || c END,
    '/tilesets/mana-seed/village-accessories/village laundry, post angle 48x64.png',
    c * 48, 0, 48, 64, 1, 0
FROM generate_series(0, 6) c;  -- 0..6, skip col 7 (duplicate of col 0)

-- ========== Tag all laundry states as rotatable ==========

INSERT INTO asset_state_tag (state_id, tag)
SELECT s.id, 'rotatable'
FROM asset_state s JOIN asset a ON a.id = s.asset_id
WHERE a.name LIKE 'Laundry (%' AND a.pack_id = 'mana-seed';
