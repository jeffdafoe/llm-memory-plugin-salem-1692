-- LLM-655: retire the coin-a-day town rate (LLM-557). The estate rate (LLM-652/653)
-- is the constable's one levy: taken by the engine into the town chest when he
-- calls at a keeper's business on his rounds, and the chest pays his wage.
--
-- The day's rate accrued a per-business balance (village_object.rate_owed) that
-- the keeper settled by hand through pay. Nothing reads or writes the column any
-- more, so it goes; whatever arrears it held (three shops at 1 coin on
-- 2026-09-10) are forgiven with it. The two knobs (town_rate_coins_per_day,
-- town_rate_max_owed) left the settings registry, so their persisted rows are
-- dead keys — removed rather than left to read as live tunables.
--
-- Historical agent_action_log `paid` rows carrying `rate_settled` are untouched:
-- the coin-record seed and the api-side narration still read them as dues.
--
-- ENGINE-OWNED tables; deploy.sh runs stop -> migrate -> start, so this applies
-- engine-STOPPED. Rerun-safe: DROP COLUMN IF EXISTS, DELETE by key. Loud
-- validation at the end.

BEGIN;

ALTER TABLE village_object DROP COLUMN IF EXISTS rate_owed;

DELETE FROM setting WHERE key IN ('town_rate_coins_per_day', 'town_rate_max_owed');

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.columns
                WHERE table_name = 'village_object' AND column_name = 'rate_owed') THEN
        RAISE EXCEPTION 'LLM-655: village_object.rate_owed still present after DROP';
    END IF;
END $$;

COMMIT;
