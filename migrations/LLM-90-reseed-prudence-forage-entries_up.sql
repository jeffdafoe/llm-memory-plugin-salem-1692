-- LLM-90: re-seed Prudence Ward's herbalist forage restock entries (raspberries +
-- blueberries, cap 10), so the "## Your bushes to harvest" cue + the forage
-- restock warrant activate for her. Identical payload to LLM-59-prudence-forage-
-- entries_up; it lives under a NEW basename because LLM-59 is already recorded in
-- migrations_applied (its hotfix revert was applied out-of-band, not by replaying
-- the _down through the runner), so the runner would never re-apply LLM-59's _up.
--
-- WHY re-enable now: LLM-59's original cue drove a gather-from-afar reject loop.
-- That root cause is fixed (LLM-79 re-sourced the cue from earned memory and
-- steers move_to only), and LLM-90 added the missing wake (forage warrant, gated
-- on a remembered owned bush so it can't wake-loop) and the duty-steer handling
-- (the to-work arm defers a harvest trip; the at-post stabilizer steps her out
-- instead of pinning her), plus a don't-abandon-a-customer guard. With those in,
-- the forage loop is safe to turn on. See
-- shared/notes/codebase/salem-engine-v2/forage-restock.
--
-- The restock policy is the union of every attribute's params.restock; the
-- `herbalist` role is the home for her grow-to-sell entries.
--
-- actor_attribute is CHECKPOINT-WRITTEN by the engine (raw params bytes written
-- back verbatim, actors.go SaveSnapshot step 30). The deploy now runs migrations
-- with the engine stopped (down -> migrate -> up), so this applies cleanly and the
-- post-deploy boot loads the new params and derives the forage RestockPolicy. (An
-- ad-hoc apply outside a deploy must still stop the engine first.)

BEGIN;

DO $$
DECLARE n int;
BEGIN
    UPDATE actor_attribute
    SET params = '{"restock": [{"item": "raspberries", "source": "forage", "max": 10}, {"item": "blueberries", "source": "forage", "max": 10}]}'::jsonb
    WHERE actor_id = '019dbcec-1149-7149-8a49-2cdb54680b86'  -- Prudence Ward
      AND slug = 'herbalist';
    GET DIAGNOSTICS n = ROW_COUNT;
    -- 0 rows is the unseeded case (fresh schema-only DB / integration harness) —
    -- fine. But if Prudence exists yet has no herbalist row, the feature would
    -- silently stay inactive; fail loud so a stale actor id / missing role row is
    -- caught at deploy.
    IF n = 0 AND EXISTS (SELECT 1 FROM actor WHERE id = '019dbcec-1149-7149-8a49-2cdb54680b86') THEN
        RAISE EXCEPTION 'LLM-90: Prudence exists but her herbalist actor_attribute row was not found';
    END IF;
END $$;

COMMIT;
