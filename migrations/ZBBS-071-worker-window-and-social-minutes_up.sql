-- ZBBS-071: replace worker schedule offset with absolute window;
-- convert social schedule from hours to minutes.
--
-- Worker:
--   schedule_offset_minutes (single ±value trimming dawn/dusk) →
--   schedule_start_minute / schedule_end_minute (absolute minutes-of-day,
--   1-min precision). NULL pair = "use global dawn/dusk" at evaluation
--   time so a global dawn/dusk change is felt without re-stamping NPCs.
--   Window wraps midnight when start > end (tavernkeeper 17:00–05:00).
--
-- Social:
--   social_start_hour / social_end_hour (integer hours) →
--   social_start_minute / social_end_minute (integer minutes 0–1439).
--   Existing values multiplied ×60 so on-the-hour windows preserve byte
--   for byte. The all-or-none CHECK is rebuilt against the new columns.
--
-- Lateness window column (lateness_window_minutes) unchanged.

BEGIN;

-- New absolute-window columns. Both NULL = inherit dawn/dusk at eval time.
ALTER TABLE npc
    ADD COLUMN schedule_start_minute SMALLINT
        CHECK (schedule_start_minute IS NULL
               OR (schedule_start_minute >= 0 AND schedule_start_minute <= 1439)),
    ADD COLUMN schedule_end_minute SMALLINT
        CHECK (schedule_end_minute IS NULL
               OR (schedule_end_minute >= 0 AND schedule_end_minute <= 1439)),
    ADD CONSTRAINT npc_schedule_window_all_or_none CHECK (
        (schedule_start_minute IS NULL AND schedule_end_minute IS NULL)
        OR
        (schedule_start_minute IS NOT NULL AND schedule_end_minute IS NOT NULL)
    );

-- Backfill: only NPCs with a non-zero offset need materialization. Workers
-- on offset=0 used to dynamically follow dawn/dusk; leaving their new
-- columns NULL preserves that "inherit" behavior under the new model. For
-- non-zero offsets the conversion materializes the effective arrive/leave
-- so post-migration scheduling is byte-for-byte identical. Reads the live
-- world settings (default 7:00 / 19:00 if unset) the same way the Go
-- loader does. Modular arithmetic handles offsets that push a boundary
-- across midnight.
WITH cfg AS (
    SELECT
        COALESCE(
            (SELECT (split_part(value, ':', 1)::int * 60
                   + split_part(value, ':', 2)::int)
             FROM setting WHERE key = 'world_dawn_time'),
            7 * 60
        ) AS dawn_min,
        COALESCE(
            (SELECT (split_part(value, ':', 1)::int * 60
                   + split_part(value, ':', 2)::int)
             FROM setting WHERE key = 'world_dusk_time'),
            19 * 60
        ) AS dusk_min
)
UPDATE npc SET
    schedule_start_minute = ((cfg.dawn_min + npc.schedule_offset_minutes) % 1440 + 1440) % 1440,
    schedule_end_minute   = ((cfg.dusk_min - npc.schedule_offset_minutes) % 1440 + 1440) % 1440
FROM cfg
WHERE behavior = 'worker'
  AND home_structure_id IS NOT NULL
  AND work_structure_id IS NOT NULL
  AND schedule_offset_minutes <> 0;

ALTER TABLE npc
    DROP CONSTRAINT IF EXISTS npc_schedule_offset_minutes_check;

ALTER TABLE npc
    DROP COLUMN schedule_offset_minutes;

-- Convert social hours → minutes. Add the new columns, copy ×60, drop old.
ALTER TABLE npc
    ADD COLUMN social_start_minute SMALLINT
        CHECK (social_start_minute IS NULL
               OR (social_start_minute >= 0 AND social_start_minute <= 1439)),
    ADD COLUMN social_end_minute SMALLINT
        CHECK (social_end_minute IS NULL
               OR (social_end_minute >= 0 AND social_end_minute <= 1439));

UPDATE npc SET
    social_start_minute = social_start_hour * 60,
    social_end_minute = social_end_hour * 60
WHERE social_start_hour IS NOT NULL
  AND social_end_hour IS NOT NULL;

-- The social_all_or_none CHECK references the old hour columns; rebuild it
-- against the new minute columns before dropping the originals.
ALTER TABLE npc
    DROP CONSTRAINT IF EXISTS social_all_or_none;

ALTER TABLE npc
    DROP COLUMN social_start_hour,
    DROP COLUMN social_end_hour;

ALTER TABLE npc
    ADD CONSTRAINT social_all_or_none CHECK (
        (social_tag IS NULL AND social_start_minute IS NULL AND social_end_minute IS NULL)
        OR
        (social_tag IS NOT NULL AND social_start_minute IS NOT NULL AND social_end_minute IS NOT NULL)
    );

COMMIT;
