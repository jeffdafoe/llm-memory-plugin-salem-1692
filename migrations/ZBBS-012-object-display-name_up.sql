-- Add display_name column to village_object for user-assigned names
-- (e.g. "General Store", "Town Hall")
ALTER TABLE village_object ADD COLUMN display_name VARCHAR(100);
