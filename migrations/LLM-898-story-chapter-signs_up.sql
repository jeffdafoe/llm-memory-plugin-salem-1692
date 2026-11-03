-- LLM-898 (LLM-845 part 2): story chapter 2, "Signs".
--
-- Chapter 2 opens when chapter 1's last beat (arrival-5) is done, at
-- Sojourner. Its first beat brings the player's first omen on purpose: the open
-- beat is the arming, so the first well drink or camp fire lighting after the
-- offer goes wrong (trigger_args.omens). Then the player asks a villager about
-- it, buys a horseshoe, and hangs it over their tent door. Finishing signs-4
-- opens the player's random omens (sim.pcOmensOpenAfter). 50 Standing in all.
--
-- Three new trigger kinds join the CHECK list: omen_witnessed, item_acquired,
-- ward_placed (engine/sim/story.go). The file carries LLM-898 rather than
-- LLM-845 so it sorts after LLM-892, which creates story_mission.
--
-- A player who finished arrival-5 before this runs is offered signs-1 at the
-- next load (rehydrateStoryOnLoad offers a finished beat's next beat).
-- Rerun-safe: the rows insert ON CONFLICT DO NOTHING, and arrival-5 is linked
-- only while it has no next beat.
BEGIN;

ALTER TABLE public.story_mission DROP CONSTRAINT IF EXISTS story_mission_trigger_kind_check;
ALTER TABLE public.story_mission ADD CONSTRAINT story_mission_trigger_kind_check CHECK (trigger_kind IN
    ('camp_pitched', 'camp_fire_lit', 'slept_at_home', 'meal_at', 'spoke_with_villager',
     'omen_witnessed', 'item_acquired', 'ward_placed'));

INSERT INTO public.story_mission
    (id, chapter, seq, title, offer_text, done_text, already_text,
     trigger_kind, trigger_args, standing_reward, next_mission_id, is_start)
VALUES
    ('signs-1', 'Signs', 1, 'Go About Your Days',
     'Salem has taken you in, for now. Keep your camp as you have learned to: draw your water at the well, and light your fire when the evening comes on.',
     'It may be nothing. Water turns, fires sink. But it sits ill with you, and you would like to know whether anyone else has seen the like.',
     NULL,
     'omen_witnessed', '{"omens": ["camp_fire_gutters", "foul_well"]}'::jsonb, 15, 'signs-2', false),
    ('signs-2', 'Signs', 2, 'Ask About It',
     'Find one of the villagers and speak with them. Ask whether such things happen here, and what people make of them.',
     'They have heard of such things, and they have their own ideas about them. The old households keep a horseshoe over the door, for luck.',
     NULL,
     'spoke_with_villager', '{}'::jsonb, 10, 'signs-3', false),
    ('signs-3', 'Signs', 3, 'A Shoe for the Door',
     'The smith forges horseshoes, and keeps a few at his stall. Buy one.',
     'The shoe is heavy in your pack: cold iron, worn smooth at the toe.',
     'you have a horseshoe',
     'item_acquired', '{"item": "horseshoe"}'::jsonb, 10, 'signs-4', false),
    ('signs-4', 'Signs', 4, 'Over the Door',
     'Go to your tent and hang the horseshoe over the door, points up, the way the old households do.',
     'The horseshoe hangs over your door. Your neighbors will see it there, and know you keep the old ways.',
     'a horseshoe hangs over your door',
     'ward_placed', '{"item": "horseshoe"}'::jsonb, 15, NULL, false)
ON CONFLICT (id) DO NOTHING;

UPDATE public.story_mission SET next_mission_id = 'signs-1'
WHERE id = 'arrival-5' AND next_mission_id IS NULL;

COMMIT;
