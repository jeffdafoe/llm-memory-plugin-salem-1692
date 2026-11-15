-- LLM-967: the storm damage painted: snapped trees, their stumps, the broken log.
--
-- A storm (or the daily roll) brings a tree down across a road
-- (engine/sim/damage_road.go). Its looks came from the Mana Seed Growable Trees
-- and forest packs, whose licence forbids use alongside AI. tools/tree-art/
-- fallen.py now paints them from our own standing trees (approved by Jeff
-- 2026-11-15) into three packed sheets:
--   res://assets/trees/fallen-tops.png   the snapped tops, a row per species
--                                       and pose, the "bare" and "winter" tops
--                                       side by side (two trees, not seasons)
--   res://assets/trees/storm-stumps.png  the six stumps, one 29x24 frame
--   res://assets/trees/fallen-trunk.png  the broken log's six season looks, 32x32
-- The numbers below are the painter's geometry
-- (engine/sim/testdata/fallen-trees-snapped.json): each top asset's anchor and
-- footprint (measured off the new art; a footprint is one tile left, right,
-- up and down of the anchor, one side two), each frame's rect, the stump's
-- anchor at its foot. The engine's road variants carry the matching stump
-- offsets (64, +-64 world px). The log keeps its anchor and footprint.
--
-- This migration sorts after LLM-967e-apple-winter (salem migrations run in
-- filename order). When the Fallen Maple exists (a fresh database has no
-- catalog; then it does nothing) it:
--   1. sets the six top assets' anchor and footprint and points their bare /
--      winter states at their frames;
--   2. points the Storm Stump's six states at their frames, anchored at the foot;
--   3. points the Fallen Trunk's six season states at their frames. Their
--      season tags are untouched.
-- Placed stumps and tops keep their state, so they redraw in the new art.
-- Then every row must hold exactly, or the migration fails.
--
-- ENGINE-OWNED data (catalog), read at boot; deploy.sh runs
-- stop -> migrate -> start. Convergent: rerunning ends in the same place.

BEGIN;

DO $$
DECLARE
    stump CONSTANT uuid := '019e5f00-c401-7a10-9e00-000000678004';
    trunk CONSTANT uuid := '019e5f00-c401-7a10-9e00-000000678005';
    tops_sheet CONSTANT text := 'res://assets/trees/fallen-tops.png';
    stumps_sheet CONSTANT text := 'res://assets/trees/storm-stumps.png';
    trunk_sheet CONSTANT text := 'res://assets/trees/fallen-trunk.png';
    bad integer;
BEGIN
    IF NOT EXISTS (SELECT 1 FROM asset WHERE id = '019e5f00-c401-7a10-9e00-000000678001') THEN
        RETURN;
    END IF;

    -- 1. The tops: (asset, anchor, footprint left/right/top/bottom, frame size,
    -- the bare frame's row y; the winter frame sits one frame width right).
    CREATE TEMP TABLE llm967f_top (
        asset_id uuid PRIMARY KEY, anchor_x double precision NOT NULL, anchor_y double precision NOT NULL,
        fl integer NOT NULL, fr integer NOT NULL, ft integer NOT NULL, fb integer NOT NULL,
        w integer NOT NULL, h integer NOT NULL, y integer NOT NULL) ON COMMIT DROP;
    INSERT INTO llm967f_top VALUES
        ('019e5f00-c401-7a10-9e00-000000678001', 0.5,   0.5,   1, 1, 1, 2, 80, 80,   0),  -- Fallen Maple (NW)
        ('019e5f00-c401-7a10-9e00-000000789001', 0.5,   0.5,   1, 2, 1, 1, 80, 80,  80),  -- Fallen Maple (SW)
        ('019e5f00-c401-7a10-9e00-000000678002', 0.375, 0.375, 1, 1, 1, 2, 64, 64, 160),  -- Fallen Chestnut (NW)
        ('019e5f00-c401-7a10-9e00-000000789002', 0.375, 0.5,   1, 2, 1, 1, 64, 80, 224),  -- Fallen Chestnut (SW)
        ('019e5f00-c401-7a10-9e00-000000678003', 0.5,   0.5,   1, 1, 1, 1, 80, 80, 304),  -- Fallen Birch (NW)
        ('019e5f00-c401-7a10-9e00-000000789003', 0.5,   0.5,   1, 1, 1, 1, 80, 80, 384);  -- Fallen Birch (SW)

    -- Every state the new art draws: (asset, state, sheet, rect).
    CREATE TEMP TABLE llm967f_state (
        asset_id uuid NOT NULL, state text NOT NULL, sheet text NOT NULL,
        src_x integer NOT NULL, src_y integer NOT NULL, src_w integer NOT NULL, src_h integer NOT NULL,
        PRIMARY KEY (asset_id, state)) ON COMMIT DROP;
    INSERT INTO llm967f_state
    SELECT t.asset_id, s.state, tops_sheet, s.col * t.w, t.y, t.w, t.h
      FROM llm967f_top t CROSS JOIN (VALUES ('bare', 0), ('winter', 1)) s(state, col);
    -- 2. The stumps, one 29x24 frame each, left to right.
    INSERT INTO llm967f_state
    SELECT stump, s.state, stumps_sheet, s.i * 29, 0, 29, 24
      FROM (VALUES ('maple-bare', 0), ('maple-winter', 1), ('chestnut-bare', 2), ('chestnut-winter', 3),
                   ('birch-bare', 4), ('birch-winter', 5)) s(state, i);
    -- 3. The log's season looks, 32x32 each, left to right.
    INSERT INTO llm967f_state
    SELECT trunk, s.state, trunk_sheet, s.i * 32, 0, 32, 32
      FROM (VALUES ('default', 0), ('autumn', 1), ('winter-clean', 2), ('winter-leaves', 3),
                   ('winter-snowy-top', 4), ('winter-snowy', 5)) s(state, i);

    -- The states exist (with their tags) since LLM-678 / LLM-789 / LLM-849;
    -- a catalog missing one is not the one this was authored against.
    SELECT count(*) INTO bad
      FROM llm967f_state n
     WHERE NOT EXISTS (SELECT 1 FROM asset_state s WHERE s.asset_id = n.asset_id AND s.state = n.state);
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-967f: % fallen-tree states are missing from the catalog', bad;
    END IF;

    UPDATE asset a
       SET anchor_x = t.anchor_x, anchor_y = t.anchor_y,
           footprint_left = t.fl, footprint_right = t.fr, footprint_top = t.ft, footprint_bottom = t.fb
      FROM llm967f_top t
     WHERE a.id = t.asset_id;
    UPDATE asset SET anchor_x = 0.517241, anchor_y = 0.84 WHERE id = stump;

    UPDATE asset_state s
       SET sheet = n.sheet, src_x = n.src_x, src_y = n.src_y, src_w = n.src_w, src_h = n.src_h,
           frame_count = 1, frame_rate = 0
      FROM llm967f_state n
     WHERE s.asset_id = n.asset_id AND s.state = n.state;

    -- Every state as authored, and none of these assets left on pack art.
    SELECT count(*) INTO bad
      FROM llm967f_state n
      JOIN asset_state s ON s.asset_id = n.asset_id AND s.state = n.state
     WHERE (s.sheet, s.src_x, s.src_y, s.src_w, s.src_h, s.frame_count, s.frame_rate)
           IS DISTINCT FROM (n.sheet, n.src_x, n.src_y, n.src_w, n.src_h, 1, 0);
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-967f: % fallen-tree states differ from the authored values', bad;
    END IF;
    SELECT count(*) INTO bad
      FROM asset_state s
     WHERE s.asset_id IN (SELECT asset_id FROM llm967f_top UNION ALL SELECT stump UNION ALL SELECT trunk)
       AND s.sheet NOT LIKE 'res://assets/trees/%';
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-967f: % fallen-tree states are still on pack art', bad;
    END IF;

    -- Every asset's anchor and footprint as authored.
    SELECT count(*) INTO bad
      FROM llm967f_top t JOIN asset a ON a.id = t.asset_id
     WHERE (a.anchor_x, a.anchor_y, a.footprint_left, a.footprint_right, a.footprint_top, a.footprint_bottom)
           IS DISTINCT FROM (t.anchor_x, t.anchor_y, t.fl, t.fr, t.ft, t.fb);
    IF bad > 0 OR (SELECT count(*) FROM asset WHERE id IN (SELECT asset_id FROM llm967f_top)) <> 6 THEN
        RAISE EXCEPTION 'LLM-967f: % fallen tops differ from their authored anchor and footprint', bad;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM asset WHERE id = stump AND anchor_x = 0.517241 AND anchor_y = 0.84) THEN
        RAISE EXCEPTION 'LLM-967f: the Storm Stump is not anchored at its foot';
    END IF;
END $$;

COMMIT;
