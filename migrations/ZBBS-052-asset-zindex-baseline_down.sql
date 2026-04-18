-- ZBBS-052 down — restore the original z_index baseline (0 default,
-- bridge at -1).

ALTER TABLE asset ALTER COLUMN z_index SET DEFAULT 0;

UPDATE asset SET z_index = 0 WHERE is_passage = FALSE;
UPDATE asset SET z_index = -1 WHERE is_passage = TRUE;
