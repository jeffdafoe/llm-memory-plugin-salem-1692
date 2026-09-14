-- LLM-656: the shortage peddler — standing input shortages, remembered across
-- game-days.
--
-- Once a game-day the engine sweeps every resident keeper for a produced good
-- short a required input that no village supplier holds (engine
-- shortage_peddler.go, on the rotation boundary beside the farm upkeep and the
-- constable's wage) and counts the consecutive days each shortage has stood.
-- After shortage_peddler_days (3) the visitor cascade brings in a peddler
-- carrying that good, bound to the short keeper's shop.
--
-- The count is the only new durable state. It must survive restart: the village
-- restarts several times a day for deploys, so an in-memory count would rarely
-- reach three. It rides world_state with the rest of the checkpointed
-- environment as a jsonb array of {keeper_id, item, days, last_seen_at,
-- last_peddler_at}; an empty array is the no-shortage state.
--
-- The two knobs (shortage_peddler_days 3, shortage_peddler_batches 2) are
-- registry settings with compiled defaults; they need no setting rows here and
-- are live-tunable through the umbilical (/settings/set), persisting on the next
-- checkpoint.
--
-- ENGINE-OWNED table; deploy.sh runs stop -> migrate -> start, so this applies
-- engine-STOPPED. Rerun-safe: ADD COLUMN IF NOT EXISTS. Loud validation at the
-- end.

BEGIN;

ALTER TABLE world_state ADD COLUMN IF NOT EXISTS input_shortages jsonb NOT NULL DEFAULT '[]'::jsonb;

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM information_schema.columns
                    WHERE table_name = 'world_state' AND column_name = 'input_shortages') THEN
        RAISE EXCEPTION 'LLM-656: world_state.input_shortages missing after ALTER';
    END IF;
END $$;

COMMIT;
