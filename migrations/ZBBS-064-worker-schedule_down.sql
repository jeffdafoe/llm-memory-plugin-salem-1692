BEGIN;

DELETE FROM npc_behavior WHERE slug = 'worker';

ALTER TABLE npc
    DROP CONSTRAINT schedule_all_or_none,
    DROP COLUMN last_shift_tick_at,
    DROP COLUMN active_end_hour,
    DROP COLUMN active_start_hour,
    DROP COLUMN schedule_interval_hours,
    DROP COLUMN schedule_offset_hours;

COMMIT;
