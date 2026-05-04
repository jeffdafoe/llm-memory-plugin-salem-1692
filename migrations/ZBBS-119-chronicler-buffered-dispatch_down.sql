-- ZBBS-119: Chronicler buffered dispatch — down migration.
--
-- Removes the two settings rows added by the up migration. The dispatcher
-- code reads via loadNonNegativeIntSetting and a bool helper that fall
-- back to defaults when the row is missing, so removing the rows is safe
-- as long as the engine is also rolled back to a build that doesn't
-- expect them. If you're rolling back just the migration but keeping the
-- engine, the defaults (window=60, flag=false) take over silently.

BEGIN;

DELETE FROM setting WHERE key IN (
    'chronicler_buffer_window_seconds',
    'chronicler_buffered_dispatch'
);

COMMIT;
