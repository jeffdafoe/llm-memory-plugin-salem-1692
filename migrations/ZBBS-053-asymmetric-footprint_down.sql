-- ZBBS-053 down — restore symmetric footprint_w / footprint_h.
--
-- Reverses the per-side split. Total width = left + right + 1, total
-- height = top + bottom + 1. Asymmetric-bottom isn't representable by
-- the old model (everything extended up from anchor), so any non-zero
-- footprint_bottom is silently lost in the down migration.

ALTER TABLE asset
    ADD COLUMN footprint_w INT NOT NULL DEFAULT 1 CHECK (footprint_w > 0),
    ADD COLUMN footprint_h INT NOT NULL DEFAULT 1 CHECK (footprint_h > 0);

UPDATE asset
SET footprint_w = GREATEST(1, footprint_left + footprint_right + 1),
    footprint_h = GREATEST(1, footprint_top + footprint_bottom + 1);

ALTER TABLE asset
    DROP COLUMN footprint_left,
    DROP COLUMN footprint_right,
    DROP COLUMN footprint_top,
    DROP COLUMN footprint_bottom;
