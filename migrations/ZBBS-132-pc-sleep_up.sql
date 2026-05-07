-- ZBBS-132: PC sleep mechanic — sleeping_until column, idle tracking,
-- auto-bed setting.
--
-- Stage B of the lodging design (`shared/tasks/lodging/design`). The
-- engine plumbing for "PC pays for a night, sleeps through till dawn":
--
--   actor.sleeping_until    — when the actor is asleep, this is the
--                             planned wake time (next dawn). NULL when
--                             awake. The wake-sweep goroutine clears
--                             rows whose sleeping_until <= NOW() and
--                             broadcasts pc_sleep_ended.
--
--   actor.last_pc_input_at  — UTC timestamp of the most recent PC HTTP
--                             call. Updated by every PC handler entry
--                             via app.touchPCInput. The auto-bed scan
--                             reads this to decide whether a connected
--                             PC has been idle long enough to bed.
--
--   pc_idle_sleep_minutes   — settings row. How many minutes of input
--                             silence before auto-bed fires. 5min is a
--                             balance: long enough that brief AFK
--                             (bathroom break, side conversation) doesn't
--                             trigger the sleep state, short enough that
--                             a player who actually walked away gets the
--                             rested-at-dawn payoff without manually
--                             clicking "sleep now."
--
-- Auto-bed only fires when ALL of: PC has active lodger status, PC is
-- inside a structure where they're a lodger, last_pc_input_at is past
-- the threshold, sleeping_until IS NULL. The check is read-only at the
-- pay_ledger lookup; sleep state is materialized via these two columns.

BEGIN;

ALTER TABLE actor
    ADD COLUMN sleeping_until    TIMESTAMPTZ,
    ADD COLUMN last_pc_input_at  TIMESTAMPTZ;

INSERT INTO setting (key, value, description, is_public) VALUES
    ('pc_idle_sleep_minutes', '5', 'Minutes of PC HTTP-input silence before a lodger PC is auto-bedded (set to a high value to disable auto-bed)', false);

COMMIT;
