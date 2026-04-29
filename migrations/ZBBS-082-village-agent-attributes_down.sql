BEGIN;

DELETE FROM setting WHERE key IN (
    'attribute_tick_amount',
    'meal_drop',
    'drink_drop',
    'last_attribute_tick_hour'
);

ALTER TABLE village_agent ALTER COLUMN coins SET DEFAULT 100;

ALTER TABLE village_agent DROP COLUMN tiredness;
ALTER TABLE village_agent DROP COLUMN thirst;
ALTER TABLE village_agent DROP COLUMN hunger;

COMMIT;
