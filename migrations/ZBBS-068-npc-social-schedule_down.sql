BEGIN;

ALTER TABLE npc
    DROP CONSTRAINT IF EXISTS social_all_or_none,
    DROP COLUMN IF EXISTS social_last_boundary_at,
    DROP COLUMN IF EXISTS social_end_hour,
    DROP COLUMN IF EXISTS social_start_hour,
    DROP COLUMN IF EXISTS social_tag;

COMMIT;
