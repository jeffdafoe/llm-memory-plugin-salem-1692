-- LLM-843: omens — the record of who is armed and who had one when.
--
-- Once a game-day the engine may arm one omen against a resident (engine
-- omen.go, on the rotation boundary beside the damage roll); it happens at that
-- person's next milking, stoke, bake, berry harvest or draw at a well. A
-- resident who had one is passed over for omen_cooldown_days.
--
-- The record must survive restart: the cooldown is counted in days and an armed
-- omen waits up to omen_lapse_hours for its moment, while the village restarts
-- several times a day. It rides world_state with the rest of the checkpointed
-- environment as a jsonb object {armed: [...], last_at: {...}}; '{}' is the
-- empty record.
--
-- The three knobs (omen_chance_permille 250, omen_cooldown_days 7,
-- omen_lapse_hours 24) are registry settings with compiled defaults; they need
-- no setting rows here and are live-tunable through the umbilical
-- (/settings/set), persisting on the next checkpoint.
--
-- ENGINE-OWNED table; deploy.sh runs stop -> migrate -> start, so this applies
-- engine-STOPPED. Rerun-safe: ADD COLUMN IF NOT EXISTS. Loud validation at the
-- end.

BEGIN;

ALTER TABLE world_state ADD COLUMN IF NOT EXISTS omens jsonb NOT NULL DEFAULT '{}'::jsonb;

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM information_schema.columns
                    WHERE table_name = 'world_state' AND column_name = 'omens') THEN
        RAISE EXCEPTION 'LLM-843: world_state.omens missing after ALTER';
    END IF;
END $$;

COMMIT;
