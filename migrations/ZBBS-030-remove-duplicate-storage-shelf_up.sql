-- ZBBS-030: Remove Storage Shelf assets — they duplicate Log Rack sprites.
--
-- Row 3 cols 0-3 of village accessories 32x32.png visually match row 4
-- (the Log Rack fill progression). Jeff confirmed via viewer. All 4 Storage
-- Shelf* rows have 0 placements; safe to delete.

DELETE FROM asset_state
WHERE asset_id IN (SELECT id FROM asset WHERE name LIKE 'Storage Shelf%');

DELETE FROM asset
WHERE name LIKE 'Storage Shelf%';
