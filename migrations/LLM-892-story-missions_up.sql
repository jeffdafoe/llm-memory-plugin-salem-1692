-- LLM-892: player story missions, Standing and ranks.
--
-- Salem is told as an interactive story. A player works through story beats
-- (missions) grouped in chapters: each beat has a finish condition the engine
-- watches for, gives Standing when done, and offers the next beat.
--
-- story_mission is authored reference data: the engine loads it at boot and
-- never writes it. pc_story_mission is each player's progress, checkpointed by
-- the engine with the generation-marker sweep (a reset drops a player's rows).
-- actor.standing is the player's Standing; actor.standing_rank_seen is the
-- highest rank whose popup the player has closed.
--
-- pc_story_mission names actors and missions by soft text refs (no FK),
-- validated in Go at load, like door_note and labor_contract.
-- Engine-checkpointed → deploy stop -> migrate -> start.
BEGIN;

CREATE TABLE IF NOT EXISTS public.story_mission (
    id               text    PRIMARY KEY CHECK (id ~ '^[a-z0-9]+(-[a-z0-9]+)*$'),
    chapter          text    NOT NULL,
    seq              integer NOT NULL,
    title            text    NOT NULL,
    offer_text       text    NOT NULL,
    done_text        text    NOT NULL,
    already_text     text,
    trigger_kind     text    NOT NULL CHECK (trigger_kind IN
        ('camp_pitched', 'camp_fire_lit', 'slept_at_home', 'meal_at', 'spoke_with_villager')),
    trigger_args     jsonb   NOT NULL DEFAULT '{}'::jsonb,
    standing_reward  integer NOT NULL DEFAULT 0 CHECK (standing_reward >= 0),
    next_mission_id  text    REFERENCES public.story_mission (id) DEFERRABLE INITIALLY DEFERRED,
    is_start         boolean NOT NULL DEFAULT false
);

CREATE TABLE IF NOT EXISTS public.pc_story_mission (
    actor_id        text        NOT NULL,
    mission_id      text        NOT NULL,
    offered_at      timestamptz NOT NULL,
    finished_at     timestamptz,
    offer_seen_at   timestamptz,
    finish_seen_at  timestamptz,
    caught_up       boolean     NOT NULL DEFAULT false,
    snapshot_gen    bigint      NOT NULL,
    PRIMARY KEY (actor_id, mission_id)
);

CREATE SEQUENCE IF NOT EXISTS public.pc_story_mission_snapshot_gen_seq;

ALTER TABLE public.actor ADD COLUMN IF NOT EXISTS standing integer NOT NULL DEFAULT 0;
ALTER TABLE public.actor ADD COLUMN IF NOT EXISTS standing_rank_seen integer NOT NULL DEFAULT 0;

INSERT INTO public.story_mission
    (id, chapter, seq, title, offer_text, done_text, already_text,
     trigger_kind, trigger_args, standing_reward, next_mission_id, is_start)
VALUES
    ('arrival-1', 'Arrival', 1, 'Make Camp',
     'The common room at the Inn is crowded, and a bed there costs coin. You carry a tent. Find a patch of open grass, away from the doors and the pens, and pitch it.',
     'Your tent stands. Whatever else this place gives you, you have a roof of your own.',
     'your tent stands',
     'camp_pitched', '{}'::jsonb, 10, 'arrival-2', true),
    ('arrival-2', 'Arrival', 2, 'A Fire of Your Own',
     'A camp without a fire is a cold camp. Strike a spark at your fire ring and coax it into flame. You will sleep better beside it.',
     'The fire catches and holds. Smoke rises over your camp, and anyone on the road can see that someone lives here now.',
     'your fire burns',
     'camp_fire_lit', '{}'::jsonb, 10, 'arrival-3', false),
    ('arrival-3', 'Arrival', 3, 'A Night Under Canvas',
     'When the light goes, crawl into your tent and sleep there. The ground is hard, but it is yours.',
     'You wake stiff but dry. You have spent a night in Salem, and the village has not turned you out.',
     NULL,
     'slept_at_home', '{}'::jsonb, 10, 'arrival-4', false),
    ('arrival-4', 'Arrival', 4, 'Bread and a Bench',
     'Hunger will find you soon enough. The Inn serves hot food to anyone with coin. Go there, buy a meal, and eat it at the table.',
     'A full belly and a warm room. The keeper has seen your face now, and will know it again.',
     NULL,
     'meal_at', '{"tag": "lodging"}'::jsonb, 10, 'arrival-5', false),
    ('arrival-5', 'Arrival', 5, 'A Word with Your Neighbors',
     'No one in Salem knows who you are yet. Find one of the villagers and talk with them.',
     'You have spoken with your neighbors, and they with you. In a village this small, word of you will travel.',
     NULL,
     'spoke_with_villager', '{}'::jsonb, 10, NULL, false)
ON CONFLICT (id) DO NOTHING;

COMMIT;
