-- ZBBS-119: chronicler tick budget — down migration.
--
-- Removes the chronicler_tick_budget setting row. The engine reads via
-- loadNonNegativeIntSetting with default 8, so removing the row is
-- safe — the in-code default takes over silently. Rolling back the
-- engine to a build that uses the in-code constant (4) is also safe;
-- the setting is just ignored if the engine doesn't read it.

BEGIN;

DELETE FROM setting WHERE key = 'chronicler_tick_budget';

COMMIT;
