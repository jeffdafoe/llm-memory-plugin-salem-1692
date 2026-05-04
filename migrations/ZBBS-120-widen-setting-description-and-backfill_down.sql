-- ZBBS-120 down — best-effort revert.
--
-- Drops the three setting rows. Leaves the description column as TEXT;
-- narrowing back to varchar(255) would fail if any row's description is
-- now longer than 255 chars (which is exactly the situation this
-- migration was created to fix), and the wider type is harmless.

BEGIN;

DELETE FROM setting WHERE key IN (
    'chronicler_buffer_window_seconds',
    'chronicler_buffered_dispatch',
    'chronicler_tick_budget'
);

COMMIT;
