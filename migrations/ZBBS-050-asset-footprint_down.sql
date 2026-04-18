-- ZBBS-050 down — drop footprint columns.

ALTER TABLE asset
    DROP COLUMN footprint_w,
    DROP COLUMN footprint_h;
