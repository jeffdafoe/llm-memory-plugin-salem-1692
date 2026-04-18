-- ZBBS-053: Per-side footprint counts.
--
-- footprint_w / footprint_h were symmetric around the anchor (centered on
-- anchor X, extending up from anchor Y). For real assets that's wrong —
-- a building's roof drawn in 2.5D-perspective inflates sprite_h, and the
-- old "depth = height" rule blocked corridors NPCs needed to use. The
-- four new columns let each side be tuned independently from the editor
-- (drag the visible footprint border in or out per side).
--
-- Anchor tile is always part of the footprint. The four columns count
-- additional tiles in each cardinal direction from the anchor tile, so
-- a {left=2, right=2, top=2, bottom=0} footprint covers a 5×3 rectangle
-- with the anchor at its bottom-center.
--
-- Mapping from the old footprint_w / footprint_h:
--   left   = footprint_w / 2
--   right  = footprint_w - left - 1
--   top    = footprint_h - 1
--   bottom = 0
-- which preserves the prior (asymmetric-by-one for even widths) behavior.

ALTER TABLE asset
    ADD COLUMN footprint_left   INT NOT NULL DEFAULT 0 CHECK (footprint_left   >= 0),
    ADD COLUMN footprint_right  INT NOT NULL DEFAULT 0 CHECK (footprint_right  >= 0),
    ADD COLUMN footprint_top    INT NOT NULL DEFAULT 0 CHECK (footprint_top    >= 0),
    ADD COLUMN footprint_bottom INT NOT NULL DEFAULT 0 CHECK (footprint_bottom >= 0);

UPDATE asset
SET footprint_left   = footprint_w / 2,
    footprint_right  = footprint_w - (footprint_w / 2) - 1,
    footprint_top    = footprint_h - 1,
    footprint_bottom = 0;

ALTER TABLE asset
    DROP COLUMN footprint_w,
    DROP COLUMN footprint_h;
