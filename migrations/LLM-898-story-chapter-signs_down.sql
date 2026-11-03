-- LLM-898 down: remove story chapter 2 ("Signs") and its trigger kinds.
-- Players' progress rows on the chapter go with it (the engine would drop them
-- at load anyway, as rows on an unknown mission).
BEGIN;

UPDATE public.story_mission SET next_mission_id = NULL
WHERE id = 'arrival-5' AND next_mission_id = 'signs-1';

DELETE FROM public.pc_story_mission WHERE mission_id IN ('signs-1', 'signs-2', 'signs-3', 'signs-4');
DELETE FROM public.story_mission WHERE id IN ('signs-1', 'signs-2', 'signs-3', 'signs-4');

-- next_mission_id is a deferred FK; fire its checks now, since ALTER TABLE
-- refuses a table with pending trigger events.
SET CONSTRAINTS ALL IMMEDIATE;

ALTER TABLE public.story_mission DROP CONSTRAINT IF EXISTS story_mission_trigger_kind_check;
ALTER TABLE public.story_mission ADD CONSTRAINT story_mission_trigger_kind_check CHECK (trigger_kind IN
    ('camp_pitched', 'camp_fire_lit', 'slept_at_home', 'meal_at', 'spoke_with_villager'));

COMMIT;
