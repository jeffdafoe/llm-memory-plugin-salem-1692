-- LLM-895: the Standing ladder as data, the rank-seen mark as a rank id, and
-- the nine ranks of a long story.
--
-- WHY. LLM-892 kept four ranks in code with one setting per threshold, and
-- actor.standing_rank_seen held the POSITION of the last rank announced, so a
-- rank inserted mid-ladder would have shifted every player past it onto the
-- wrong rank. Ranks are now rows here (authored like story_mission; the
-- operator moves a threshold live with /umbilical/story/rank/set, which writes
-- this table), and standing_rank_seen holds the rank's id, so a rank can go
-- anywhere. Standing can also fall now (the engine's adjustStanding): the rank
-- follows it down, and down_text is the line the player reads when it does.
--
-- The ladder (Jeff, 2026-11-03), about 50 Standing a chapter:
--   stranger 0, sojourner 50, inhabitant 150, householder 300, goodman 500,
--   freeman 750, church_member 1000, tithingman 1300, selectman 1700.
-- "Goodman / Goodwife" is a placeholder until the player chooses how the
-- village addresses them (LLM-896).
--
-- standing_rank_seen: integer position → rank id. The LLM-892 positions map
-- onto the same ranks by id (0 stranger, 1 sojourner, 2 inhabitant, 3
-- freeman). The LLM-892 thresholds were live settings (standing_rank_*); any
-- persisted value is dropped, the table is the ladder now.
--
-- actor is engine-checkpointed → deploy stop -> migrate -> start.
BEGIN;

CREATE TABLE IF NOT EXISTS public.standing_rank (
    id           text    PRIMARY KEY CHECK (id ~ '^[a-z0-9]+(_[a-z0-9]+)*$'),
    name         text    NOT NULL,
    at_standing  integer NOT NULL CHECK (at_standing >= 0),
    up_text      text    NOT NULL DEFAULT '',
    down_text    text    NOT NULL DEFAULT '',
    prose        text[]  NOT NULL DEFAULT '{}'
);

INSERT INTO public.standing_rank (id, name, at_standing, up_text, down_text, prose) VALUES
    ('stranger', 'Stranger', 0, '', '', ARRAY[
        'No one here knows your name.',
        'A few villagers have started to know your face.',
        'People nod to you on the road now.']),
    ('sojourner', 'Sojourner', 50,
        'The town has marked you a Sojourner.',
        'The town no longer counts you a Sojourner. You are a stranger here again.', ARRAY[
        'The village tolerates you, for now.',
        'Folk have started to ask after you.',
        'You are spoken of as one who may stay.']),
    ('inhabitant', 'Inhabitant', 150,
        'The town has admitted you as an Inhabitant.',
        'The town no longer counts you among its Inhabitants.', ARRAY[
        'You are counted among the town''s people.',
        'Your word carries some weight at the well.',
        'Men of standing greet you by name.']),
    ('householder', 'Householder', 300,
        'You are a Householder now, with a hearth and ground the town knows for yours.',
        'The town no longer reckons you a Householder.', ARRAY[
        'Your door is one the neighbors know.',
        'Folk send their children to your door on errands.',
        'Your name is put down when the rates are reckoned.']),
    ('goodman', 'Goodman / Goodwife', 500,
        'The village has begun to call you Goodman, or Goodwife, as it does its own.',
        'The village no longer gives you the good name it did.', ARRAY[
        'Your neighbors give you the good name now.',
        'You are asked to stand witness to a bargain.',
        'Folk come to you for a word of advice.']),
    ('freeman', 'Freeman', 750,
        'The town has made you a Freeman, with a voice at town meeting.',
        'The town has taken away your voice at town meeting.', ARRAY[
        'You have a voice at town meeting.',
        'Your vote is sought before the meeting sits.',
        'Men speak of you when an office falls empty.']),
    ('church_member', 'Church member', 1000,
        'You are admitted to full communion at the meeting house.',
        'The church has put you out of its fellowship.', ARRAY[
        'You sit with the covenanted at the meeting house.',
        'The minister greets you at the door by name.',
        'You are spoken of as a pillar of the church.']),
    ('tithingman', 'Tithingman', 1300,
        'The town has chosen you Tithingman, to keep order on the Sabbath and watch over ten households.',
        'The town has taken the tithingman''s staff from you.', ARRAY[
        'You carry the tithingman''s staff on the Sabbath.',
        'Your ten households look to you in a quarrel.',
        'There is talk of you at the next choosing of selectmen.']),
    ('selectman', 'Selectman', 1700,
        'The town has chosen you a Selectman, one of the men who order its affairs.',
        'The town has not chosen you Selectman again.', ARRAY[
        'You sit among the Selectmen, and the town''s affairs pass through your hands.'])
ON CONFLICT (id) DO NOTHING;

-- Only an integer column is converted, so a rerun leaves the text column as it is.
DO $$
BEGIN
    IF (SELECT data_type FROM information_schema.columns
         WHERE table_schema = 'public' AND table_name = 'actor'
           AND column_name = 'standing_rank_seen') = 'integer' THEN
        ALTER TABLE public.actor ALTER COLUMN standing_rank_seen DROP DEFAULT;
        ALTER TABLE public.actor ALTER COLUMN standing_rank_seen TYPE text USING (
            CASE standing_rank_seen
                WHEN 1 THEN 'sojourner'
                WHEN 2 THEN 'inhabitant'
                WHEN 3 THEN 'freeman'
                ELSE 'stranger'
            END);
        ALTER TABLE public.actor ALTER COLUMN standing_rank_seen SET DEFAULT 'stranger';
    END IF;
END $$;

DELETE FROM public.setting
 WHERE key IN ('standing_rank_sojourner', 'standing_rank_inhabitant', 'standing_rank_freeman');

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM public.standing_rank WHERE at_standing = 0) THEN
        RAISE EXCEPTION 'LLM-895: the ladder has no rank at 0';
    END IF;
    IF (SELECT data_type FROM information_schema.columns
         WHERE table_schema = 'public' AND table_name = 'actor'
           AND column_name = 'standing_rank_seen') <> 'text' THEN
        RAISE EXCEPTION 'LLM-895: actor.standing_rank_seen is not text';
    END IF;
END $$;

COMMIT;
