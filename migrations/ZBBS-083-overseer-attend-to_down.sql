BEGIN;

DELETE FROM setting WHERE key IN (
    'hunger_red_threshold',
    'thirst_red_threshold',
    'tiredness_red_threshold',
    'chronicler_dispatch_ceiling'
);

COMMIT;
