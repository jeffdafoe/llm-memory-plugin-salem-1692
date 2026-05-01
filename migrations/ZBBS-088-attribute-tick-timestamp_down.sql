-- Down for ZBBS-088 — restore the int hour-of-day setting and drop the
-- timestamp setting. Doesn't recover lost-hour state — the int value
-- is set to NULL and the dispatcher's first-run path stamps the
-- current hour without incrementing.

BEGIN;

INSERT INTO setting (key, value, description) VALUES
    ('last_attribute_tick_hour', NULL,
     'State row (not config). Wall-clock hour-of-day (0-23) of the most recent attribute tick. NULL = never run.')
ON CONFLICT (key) DO NOTHING;

DELETE FROM setting WHERE key = 'last_attribute_tick_at';

COMMIT;
