-- ZBBS-071 down: restore the offset model and hour-precision social fields.
--
-- The new absolute-window model is strictly more expressive than the old
-- offset model — wrap-windows and asymmetric trims can't round-trip. Best
-- effort: derive offset from (start_minute - dawn_minute), clamping to the
-- old ±1380 range. Sub-hour social windows truncate to the floor hour.

BEGIN;

-- Reintroduce the offset column nullable so the backfill UPDATE has room
-- to compute, then tighten to NOT NULL DEFAULT 0 + CHECK like the original.
ALTER TABLE npc
    ADD COLUMN schedule_offset_minutes INTEGER;

WITH cfg AS (
    SELECT
        COALESCE(
            (SELECT (split_part(value, ':', 1)::int * 60
                   + split_part(value, ':', 2)::int)
             FROM setting WHERE key = 'world_dawn_time'),
            7 * 60
        ) AS dawn_min
)
UPDATE npc SET
    schedule_offset_minutes = GREATEST(-1380, LEAST(1380,
        COALESCE(npc.schedule_start_minute - cfg.dawn_min, 0)))
FROM cfg;

UPDATE npc SET schedule_offset_minutes = 0
    WHERE schedule_offset_minutes IS NULL;

ALTER TABLE npc
    ALTER COLUMN schedule_offset_minutes SET NOT NULL,
    ALTER COLUMN schedule_offset_minutes SET DEFAULT 0,
    ADD CONSTRAINT npc_schedule_offset_minutes_check
        CHECK (schedule_offset_minutes BETWEEN -1380 AND 1380);

ALTER TABLE npc
    DROP CONSTRAINT IF EXISTS npc_schedule_window_all_or_none,
    DROP COLUMN schedule_start_minute,
    DROP COLUMN schedule_end_minute;

-- Convert social back from minutes → hours via integer division (truncates
-- toward zero, matching Postgres default). Sub-hour windows lose precision.
ALTER TABLE npc
    ADD COLUMN social_start_hour INTEGER
        CHECK (social_start_hour IS NULL
               OR (social_start_hour >= 0 AND social_start_hour <= 23)),
    ADD COLUMN social_end_hour INTEGER
        CHECK (social_end_hour IS NULL
               OR (social_end_hour >= 0 AND social_end_hour <= 23));

UPDATE npc SET
    social_start_hour = social_start_minute / 60,
    social_end_hour = social_end_minute / 60
WHERE social_start_minute IS NOT NULL
  AND social_end_minute IS NOT NULL;

ALTER TABLE npc
    DROP CONSTRAINT IF EXISTS social_all_or_none;

ALTER TABLE npc
    DROP COLUMN social_start_minute,
    DROP COLUMN social_end_minute;

ALTER TABLE npc
    ADD CONSTRAINT social_all_or_none CHECK (
        (social_tag IS NULL AND social_start_hour IS NULL AND social_end_hour IS NULL)
        OR
        (social_tag IS NOT NULL AND social_start_hour IS NOT NULL AND social_end_hour IS NOT NULL)
    );

COMMIT;
