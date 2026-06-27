-- LLM-150 rollback: re-add the social-scheduler columns + their CHECK
-- constraints to actor, restoring the pre-LLM-150 baseline definition. Manual
-- rollback only -- the deploy runner applies *_up.sql, not *_down.sql. The
-- columns come back empty (all NULL); no prior values are restored (the feature
-- was never configured on any actor).

BEGIN;

ALTER TABLE actor
    ADD COLUMN IF NOT EXISTS social_tag character varying(64),
    ADD COLUMN IF NOT EXISTS social_start_minute smallint,
    ADD COLUMN IF NOT EXISTS social_end_minute smallint,
    ADD COLUMN IF NOT EXISTS social_last_boundary_at timestamp with time zone;

ALTER TABLE actor
    ADD CONSTRAINT actor_social_all_or_none CHECK ((((social_tag IS NULL) AND (social_start_minute IS NULL) AND (social_end_minute IS NULL)) OR ((social_tag IS NOT NULL) AND (social_start_minute IS NOT NULL) AND (social_end_minute IS NOT NULL)))),
    ADD CONSTRAINT actor_social_end_minute_check CHECK (((social_end_minute IS NULL) OR ((social_end_minute >= 0) AND (social_end_minute <= 1439)))),
    ADD CONSTRAINT actor_social_start_minute_check CHECK (((social_start_minute IS NULL) OR ((social_start_minute >= 0) AND (social_start_minute <= 1439))));

COMMIT;
