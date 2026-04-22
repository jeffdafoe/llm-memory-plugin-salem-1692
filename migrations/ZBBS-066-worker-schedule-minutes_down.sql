-- ZBBS-066 down: revert schedule_offset back to hours.
--
-- CAUTION: fractional offsets (anything not divisible by 60) will
-- truncate on the way down. This is the expected tradeoff — the up
-- direction widened precision, rolling back loses what the widened
-- precision let you express.

BEGIN;

ALTER TABLE npc
    DROP CONSTRAINT IF EXISTS npc_schedule_offset_minutes_check;

-- Integer division truncates toward zero, matching Postgres default.
UPDATE npc SET schedule_offset_minutes = schedule_offset_minutes / 60;

ALTER TABLE npc
    RENAME COLUMN schedule_offset_minutes TO schedule_offset_hours;

ALTER TABLE npc
    ADD CONSTRAINT npc_schedule_offset_hours_check
        CHECK (schedule_offset_hours BETWEEN -23 AND 23);

COMMIT;
