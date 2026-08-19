-- LLM-637: the ranch-style fence as a placeable asset.
--
-- ONE asset, MANY states. The Mana Seed "Fences & Walls" pack draws the ranch
-- fence as a 16x16 sheet laid out as a 3x3 pen template (corners, edges, empty
-- interior) plus a standalone vertical run in column 3. Each piece becomes one
-- asset_state on a single "Ranch Fence" asset, so the editor sidebar shows one
-- fence, and a placed segment picks its piece by state. The fence-run tool
-- (sim.PlaceFenceRun, engine/sim/fence.go) mints one village_object per tile
-- and chooses the state per tile from the shape of the drag.
--
-- THE TAG, NOT THE STATE NAME, IS THE CONTRACT. fence.go resolves pieces with
-- asset.StateForTag("fence-…"), the same way berry_state.go resolves growth
-- stages. State names match the tags here for legibility only. A state may
-- carry several tags where one cell serves several roles: the bottom-left
-- corner cell (post with a foot, rails to the right) is also the left end of a
-- free-standing horizontal line, and the vertical-run bottom cell (a post with
-- a foot) is also the lone post a 1x1 placement drops.
--
-- ONE 16x16 CELL IS ONE WORLD TILE. Objects draw at render scale 2.0, so a cell
-- covers exactly 32x32 world pixels — a tile. The asset is an obstacle with the
-- default footprint (the anchor tile only), so a closed ring of segments is a
-- sealed pen for every walker with no new pathfinding mechanism: pathfind.go
-- stamps each segment's tile impassable and A* is 4-connected.
--
-- anchor 0.5/0.85 is the standing default. PlaceFenceRun positions each segment
-- at tile origin + anchor x 32, so the sprite's top-left lands exactly on the
-- tile's top-left and WorldPos.Tile() floors back to the same tile.
--
-- SKIPPED DELIBERATELY (see LLM-637): the 32x32 angle pieces (they overlap
-- neighbouring tiles and do not fit one-tile-one-obstacle), the broken variants
-- in columns 4-6 (those land with the wear slice, as states on this same
-- asset), the narrow left-edge post (col 0, row 1) and the rails-only mid
-- (col 1, row 3).
--
-- asset / asset_state / asset_state_tag are REFERENCE data: load-only, no
-- snapshot_gen, never checkpoint-clobbered, so this needs no engine stop. The
-- catalog is boot-loaded, so the rows render after the next restart, which the
-- deploy performs anyway.
--
-- The sheet is already on the box: the pack's gates were seeded by the original
-- ZBBS-006a catalog and render off
-- /var/www/llm-memory-salem-1692/tilesets/mana-seed/fences-walls/. The spaced
-- filename is the same path convention the placed gate uses today.

BEGIN;

-- The live pack row predates this migration (the original catalog seed wrote
-- it for the gates), but the pg integration harness replays migrations on a
-- schema-only baseline with no seed rows, so insert-if-absent with the live
-- row's metadata, then assert the row we ended up with is the row we meant
-- (DO NOTHING is silent about why it did nothing; an asset attached to a
-- mismatched pack fails silently -- no FK -- and mislabels the sidebar).
INSERT INTO tileset_pack (id, name, url, pack_group, pack_source)
VALUES ('mana-seed-fences', 'Fences & Walls', 'https://seliel-the-shaper.itch.io/mana-seed',
        'mana-seed', 'https://seliel-the-shaper.itch.io')
ON CONFLICT (id) DO NOTHING;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM tileset_pack
         WHERE id = 'mana-seed-fences'
           AND (name, url) IS NOT DISTINCT FROM
               ('Fences & Walls', 'https://seliel-the-shaper.itch.io/mana-seed')
    ) THEN
        RAISE EXCEPTION
            'LLM-637: tileset_pack mana-seed-fences is missing or carries different metadata; resolve it before applying';
    END IF;
END $$;

-- Fixed UUID so the _down can find it again. Suffix is <ticket><ordinal>.
-- Permanently reserved for this asset; never re-point it.
INSERT INTO asset (
    id, name, category, default_state, anchor_x, anchor_y, layer, pack_id,
    z_index, is_obstacle, source_file)
VALUES
    ('019e5f00-c401-7a10-9e00-000000637001',
     'Ranch Fence', 'fence', 'h',
     0.5, 0.85, 'objects', 'mana-seed-fences',
     10, true,
     'ranch style fence 16x16.png');

-- Cells are (column, row) on the 16x16 grid of the 112x64 sheet.
INSERT INTO asset_state (
    asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT '019e5f00-c401-7a10-9e00-000000637001', v.state,
       '/tilesets/mana-seed/fences-walls/ranch style fence 16x16.png',
       v.col * 16, v.row * 16, 16, 16, 1, 0
  FROM (VALUES
        ('corner-tl', 0, 0),   -- post continues down, rails to the right
        ('h',         1, 0),   -- post with rails both sides; every top/bottom/line mid
        ('corner-tr', 2, 0),   -- rails from the left, post continues down
        ('v-top',     3, 0),   -- standalone vertical run: top post
        ('v',         3, 1),   -- vertical mid; also both side edges of a rectangle
        ('v-bottom',  3, 2),   -- vertical run bottom (post with a foot); also the lone post
        ('corner-bl', 0, 2),   -- post with a foot, rails to the right; also a line's left end
        ('corner-br', 2, 2)    -- rails from the left, post with a foot; also a line's right end
       ) AS v(state, col, row);

INSERT INTO asset_state_tag (state_id, tag)
SELECT s.id, t.tag
  FROM asset_state s
  JOIN (VALUES
        ('corner-tl', 'fence-corner-tl'),
        ('h',         'fence-h'),
        ('corner-tr', 'fence-corner-tr'),
        ('v-top',     'fence-v-top'),
        ('v',         'fence-v'),
        ('v-bottom',  'fence-v-bottom'),
        ('v-bottom',  'fence-post'),
        ('corner-bl', 'fence-corner-bl'),
        ('corner-bl', 'fence-end-left'),
        ('corner-br', 'fence-corner-br'),
        ('corner-br', 'fence-end-right')
       ) AS t(state, tag) ON t.state = s.state
 WHERE s.asset_id = '019e5f00-c401-7a10-9e00-000000637001';

COMMIT;
