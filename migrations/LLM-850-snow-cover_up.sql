-- LLM-850: the snow cover — how much snow lies on the village.
--
-- One village-wide level, 0..10000 (the cover ×100, engine snow.go). It builds
-- while a storm runs in winter and melts on clear skies. It rides world_state
-- with the rest of the checkpointed environment because the village restarts
-- several times a day, and a restart must not melt the snow. 0 is no snow.
--
-- The six knobs (snow_build_per_minute_x100 300, snow_melt_day_per_minute_x100
-- 5, snow_melt_night_per_minute_x100 1, snow_tier_dusting 1, snow_tier_light 20,
-- snow_tier_deep 60) are registry settings with compiled defaults; they need no
-- setting rows here and are live-tunable through the umbilical (/settings/set).
--
-- ENGINE-OWNED table; deploy.sh runs stop -> migrate -> start, so this applies
-- engine-STOPPED. Rerun-safe: ADD COLUMN IF NOT EXISTS. Loud validation at the
-- end.

BEGIN;

ALTER TABLE world_state ADD COLUMN IF NOT EXISTS snow_cover_x100 integer NOT NULL DEFAULT 0;

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM information_schema.columns
                    WHERE table_name = 'world_state' AND column_name = 'snow_cover_x100') THEN
        RAISE EXCEPTION 'LLM-850: world_state.snow_cover_x100 missing after ALTER';
    END IF;
END $$;

COMMIT;
