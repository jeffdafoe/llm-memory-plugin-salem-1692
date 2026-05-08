-- ZBBS-172 — dwell recovery mechanic (rollback)

BEGIN;

DROP TABLE IF EXISTS actor_dwell_credit;

ALTER TABLE item_satisfies
    DROP CONSTRAINT IF EXISTS item_satisfies_dwell_amount_positive,
    DROP CONSTRAINT IF EXISTS item_satisfies_dwell_period_positive,
    DROP CONSTRAINT IF EXISTS item_satisfies_dwell_total_ticks_positive,
    DROP CONSTRAINT IF EXISTS item_satisfies_dwell_triple,
    DROP COLUMN IF EXISTS dwell_amount,
    DROP COLUMN IF EXISTS dwell_period_minutes,
    DROP COLUMN IF EXISTS dwell_total_ticks;

ALTER TABLE object_refresh
    DROP CONSTRAINT IF EXISTS object_refresh_dwell_amount_negative,
    DROP CONSTRAINT IF EXISTS object_refresh_dwell_period_positive,
    DROP CONSTRAINT IF EXISTS object_refresh_dwell_pair,
    DROP COLUMN IF EXISTS dwell_amount,
    DROP COLUMN IF EXISTS dwell_period_minutes;

-- Restore the pre-migration values for the rows the up-migration touched.
UPDATE object_refresh SET amount = -4
 WHERE object_id = '019dc5f4-306f-7607-a887-a8941c4bf176'
   AND attribute = 'tiredness';

UPDATE object_refresh SET amount = -24
 WHERE object_id = '019d79ef-d9df-73d7-967a-dc202ceaf624'
   AND attribute = 'thirst';

UPDATE item_satisfies SET amount = 12
 WHERE item_kind = 'stew'
   AND attribute = 'hunger';

UPDATE village_object SET display_name = NULL
 WHERE id IN ('019d79ef-d9dc-71d0-84b7-53c64b79e98d', '019d79ef-d9dc-7b9e-a46b-56c819d0f758')
   AND display_name = 'Shade Tree';

DELETE FROM setting WHERE key = 'tiredness_critical_threshold_pct';

DELETE FROM migrations_applied WHERE migration_name = 'ZBBS-172-dwell-recovery';

COMMIT;
