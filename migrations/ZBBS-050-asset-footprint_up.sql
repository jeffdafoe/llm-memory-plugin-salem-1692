-- ZBBS-050: Multi-tile obstacle footprints.
--
-- Pathfinder previously marked only the anchor tile of each obstacle as
-- impassable, so NPCs walked right through the rest of the footprint of
-- wells, wagons, houses, etc. These columns let pathfinder block the full
-- ground footprint: footprint_w tiles centered on anchor X, footprint_h
-- tiles extending north (smaller Y) from anchor Y.
--
-- Defaults to 1×1 for everything. Structures get sprite-derived footprints
-- since their visible extent matches their physical extent — a house IS
-- where the house sprite is, and NPCs shouldn't clip through it. Trees,
-- rocks, stumps, fences, and water-features keep 1×1 because their visual
-- silhouettes (tree canopies, scattered rocks) are wider than the part you
-- can't actually walk through. NPCs walking under a tree's canopy reads
-- correctly.
--
-- Sprite-pixel-to-tile conversion: source pixels × 2 (object scale) ÷ 32
-- (world tile size) = source ÷ 16, ceiled. So a 48×80 well becomes 3×5,
-- a 80×96 wagon becomes 5×6, a 128×192 small house becomes 8×12.
-- Approximate (slight over-blocking around tall sprite tops) — tune
-- per-asset later via `UPDATE asset SET footprint_h = ... WHERE name = ...`.

ALTER TABLE asset
    ADD COLUMN footprint_w INT NOT NULL DEFAULT 1 CHECK (footprint_w > 0),
    ADD COLUMN footprint_h INT NOT NULL DEFAULT 1 CHECK (footprint_h > 0);

UPDATE asset a
SET footprint_w = GREATEST(1, (s.src_w + 15) / 16),
    footprint_h = GREATEST(1, (s.src_h + 15) / 16)
FROM asset_state s
WHERE s.asset_id = a.id
  AND s.state = a.default_state
  AND a.category = 'structure'
  AND a.is_obstacle = TRUE;
