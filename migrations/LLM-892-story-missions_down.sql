-- LLM-892 down: drop player story missions, Standing and ranks.
BEGIN;

ALTER TABLE public.actor DROP COLUMN IF EXISTS standing_rank_seen;
ALTER TABLE public.actor DROP COLUMN IF EXISTS standing;

DROP SEQUENCE IF EXISTS public.pc_story_mission_snapshot_gen_seq;
DROP TABLE IF EXISTS public.pc_story_mission;
DROP TABLE IF EXISTS public.story_mission;

COMMIT;
