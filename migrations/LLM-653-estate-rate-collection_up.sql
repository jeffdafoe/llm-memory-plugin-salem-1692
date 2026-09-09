-- LLM-653: the estate rate is collected at the door — the constable takes it
-- when he calls at a keeper's business on his rounds and the keeper is there.
--
-- The levy used to be assessed by the engine at the midnight rotation, while
-- every payer slept, so nobody ever saw the line that explained the debit. It
-- now falls when the constable arrives (engine estate_rate.go, off the beat
-- credit in npc_route.go), once per game-day per payer. The one new durable
-- column is that once-a-day stamp: the village restarts many times a day for
-- deploys, and a stamp lost between two rounds would collect twice.
--
-- The chest (world_state.town_chest_coins, LLM-652) is unchanged and now pays
-- the constable a daily wage; the knob (constable_wage_per_day, default 8) is a
-- registry setting with a compiled default — no setting row needed, live-
-- tunable through the umbilical (/settings/set), persisting on checkpoint.
--
-- ENGINE-OWNED table; deploy.sh runs stop -> migrate -> start, so this applies
-- engine-STOPPED. Rerun-safe: ADD COLUMN IF NOT EXISTS. Loud validation at the
-- end.

BEGIN;

ALTER TABLE actor ADD COLUMN IF NOT EXISTS estate_rate_assessed_at timestamptz;

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM information_schema.columns
                    WHERE table_name = 'actor' AND column_name = 'estate_rate_assessed_at') THEN
        RAISE EXCEPTION 'LLM-653: actor.estate_rate_assessed_at missing after ALTER';
    END IF;
END $$;

COMMIT;
