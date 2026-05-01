-- ZBBS-088: Replace `last_attribute_tick_hour` (int 0-23) with
-- `last_attribute_tick_at` (RFC3339 timestamp).
--
-- The hour-of-day storage couldn't distinguish a 1-hour gap from a 25-
-- hour gap, so an engine restart spanning multiple hour boundaries
-- silently lost increments — and a 24-hour outage looked like zero
-- elapsed time. Storing the full timestamp lets the dispatcher catch up
-- missed hours (capped at maxAttributeCatchupHours in code) instead of
-- only ever applying one increment per dispatch call.
--
-- The new code stamps `last_attribute_tick_at` truncated to the hour
-- boundary, so subsequent ticks fire on hour transitions exactly like
-- the old design — only the catch-up math changes.

BEGIN;

INSERT INTO setting (key, value, description) VALUES
    ('last_attribute_tick_at', NULL,
     'State row. RFC3339 timestamp of the most recent attribute tick, truncated to the hour. NULL = never run. Replaces last_attribute_tick_hour (int 0-23) which lost day-wrap information.')
ON CONFLICT (key) DO NOTHING;

DELETE FROM setting WHERE key = 'last_attribute_tick_hour';

COMMIT;
