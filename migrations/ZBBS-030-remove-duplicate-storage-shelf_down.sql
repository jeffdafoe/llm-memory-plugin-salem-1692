-- ZBBS-030 down: restore Storage Shelf assets (the row 3 duplicates).
-- This re-creates them with fresh UUIDs; any placements that existed
-- pre-ZBBS-030 (there were 0) cannot be restored.

INSERT INTO asset (id, name, category, default_state, anchor_x, anchor_y, layer, pack_id, source_file) VALUES
    (gen_random_uuid(), 'Storage Shelf', 'prop', 'default', 0.5, 0.85, 'objects', 'mana-seed', 'village accessories 32x32.png'),
    (gen_random_uuid(), 'Storage Shelf (Stocked)', 'prop', 'default', 0.5, 0.85, 'objects', 'mana-seed', 'village accessories 32x32.png'),
    (gen_random_uuid(), 'Storage Shelf (Full)', 'prop', 'default', 0.5, 0.85, 'objects', 'mana-seed', 'village accessories 32x32.png'),
    (gen_random_uuid(), 'Storage Shelf (Goods)', 'prop', 'default', 0.5, 0.85, 'objects', 'mana-seed', 'village accessories 32x32.png');

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT id, 'default', '/tilesets/mana-seed/village-accessories/village accessories 32x32.png', 0, 96, 32, 32, 1, 0 FROM asset WHERE name = 'Storage Shelf' AND pack_id = 'mana-seed'
UNION ALL
SELECT id, 'default', '/tilesets/mana-seed/village-accessories/village accessories 32x32.png', 32, 96, 32, 32, 1, 0 FROM asset WHERE name = 'Storage Shelf (Stocked)' AND pack_id = 'mana-seed'
UNION ALL
SELECT id, 'default', '/tilesets/mana-seed/village-accessories/village accessories 32x32.png', 64, 96, 32, 32, 1, 0 FROM asset WHERE name = 'Storage Shelf (Full)' AND pack_id = 'mana-seed'
UNION ALL
SELECT id, 'default', '/tilesets/mana-seed/village-accessories/village accessories 32x32.png', 96, 96, 32, 32, 1, 0 FROM asset WHERE name = 'Storage Shelf (Goods)' AND pack_id = 'mana-seed';
