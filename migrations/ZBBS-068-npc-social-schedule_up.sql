-- ZBBS-068: Per-NPC social hour schedule.
--
-- Orthogonal overlay on the existing `behavior` column: any NPC can opt
-- into a daily window where they walk to the nearest structure whose
-- asset has a state carrying `social_tag` (e.g. 'tavern') and stay there,
-- then return home at the end of the window. Doesn't replace the main
-- behavior — a worker still ends their shift at dusk, then heads out to
-- the tavern in the evening.
--
-- Model:
--   * social_tag — asset_state_tag to search for. Nearest structure whose
--     asset has any state carrying this tag wins.
--   * social_start_hour / social_end_hour — window [start, end). Wraps
--     midnight when start > end (late-night gatherings).
--   * All-or-none CHECK, same pattern as schedule_all_or_none (ZBBS-064).
--   * social_last_boundary_at — separate idempotency stamp so the social
--     scheduler doesn't collide with worker/rotation last_shift_tick_at.
--     A "boundary" here is either social_start_hour (enter) or
--     social_end_hour (leave).
--
-- Requires home_structure_id: an NPC with no home has nowhere to return to
-- at window end, so the social scheduler skips them silently.

BEGIN;

ALTER TABLE npc
    ADD COLUMN social_tag VARCHAR(64),
    ADD COLUMN social_start_hour INTEGER
        CHECK (social_start_hour IS NULL OR (social_start_hour >= 0 AND social_start_hour <= 23)),
    ADD COLUMN social_end_hour INTEGER
        CHECK (social_end_hour IS NULL OR (social_end_hour >= 0 AND social_end_hour <= 23)),
    ADD COLUMN social_last_boundary_at TIMESTAMP WITH TIME ZONE,
    ADD CONSTRAINT social_all_or_none CHECK (
        (social_tag IS NULL AND social_start_hour IS NULL AND social_end_hour IS NULL)
        OR
        (social_tag IS NOT NULL AND social_start_hour IS NOT NULL AND social_end_hour IS NOT NULL)
    );

COMMIT;
