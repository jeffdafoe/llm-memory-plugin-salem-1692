-- ZBBS-131 down: revert lodging foundation.

BEGIN;

INSERT INTO setting (key, value, description, is_public) VALUES
    ('salem_day_rate', NULL, 'vestigial — restored by ZBBS-131 down', false)
    ON CONFLICT (key) DO NOTHING;

DELETE FROM setting WHERE key IN ('lodging_check_in_hour', 'lodging_check_out_hour');

DELETE FROM item_kind WHERE name = 'nights_stay';

COMMIT;
