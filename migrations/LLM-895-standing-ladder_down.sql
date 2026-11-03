-- LLM-895 down: drop the ladder table and turn actor.standing_rank_seen back
-- into the LLM-892 position (0 stranger, 1 sojourner, 2 inhabitant, 3
-- freeman). A rank LLM-892 did not have reads as the nearest one below it.
-- The LLM-892 threshold settings come back at their defaults (no rows).

BEGIN;

DO $$
BEGIN
    IF (SELECT data_type FROM information_schema.columns
         WHERE table_schema = 'public' AND table_name = 'actor'
           AND column_name = 'standing_rank_seen') = 'text' THEN
        ALTER TABLE public.actor ALTER COLUMN standing_rank_seen DROP DEFAULT;
        ALTER TABLE public.actor ALTER COLUMN standing_rank_seen TYPE integer USING (
            CASE standing_rank_seen
                WHEN 'sojourner' THEN 1
                WHEN 'inhabitant' THEN 2
                WHEN 'householder' THEN 2
                WHEN 'goodman' THEN 2
                WHEN 'freeman' THEN 3
                WHEN 'church_member' THEN 3
                WHEN 'tithingman' THEN 3
                WHEN 'selectman' THEN 3
                ELSE 0
            END);
        ALTER TABLE public.actor ALTER COLUMN standing_rank_seen SET DEFAULT 0;
    END IF;
END $$;

DROP TABLE IF EXISTS public.standing_rank;

COMMIT;
