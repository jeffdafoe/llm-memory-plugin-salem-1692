-- ZBBS-150: sleep redesign — recovery-based wake, subspace-gated
-- auto-bed, input-wake, drop dawn-anchor.
--
-- Schema-wise this is light: two new settings rows. The substantive
-- changes are engine-side (sleep.go, tiredness_recovery_sweep.go,
-- touchPCInput, wakeExpiredSleepers).
--
-- Background: ZBBS-132's sleep mechanic anchored sleeping_until to
-- nextDawnAt(now). Combined with the auto-bed sweep firing any time of
-- day, a 5-min daytime AFK pinned the PC for ~22 hours (until next
-- dawn). IRL-frame fix: sleep is governed by tiredness recovery +
-- player choice + lodger checkout — not a fixed dawn alarm.
--
-- Wake conditions (any-of fires wakeExpiredSleepers):
--   - tiredness <= 0           (rested wake — primary)
--   - subspace_access expired  (housekeeping knock at checkout)
--   - sleeping_until <= NOW()  (safety cap)
--   - touchPCInput on /pc/*    (input wake — implicit, not on sweep)
--
-- Auto-bed gates (ZBBS-149 subspace + new tiredness threshold):
--   - PC in subspace_kind='private' (their bedroom, not the bar)
--   - tiredness >= pc_idle_sleep_min_tiredness (no auto-bed of fresh
--     PCs who just walked into their room)
--   - existing: connected, idle past pc_idle_sleep_minutes, lodger
--     status valid here.
--
-- Recovery rate reuses take_break.tiredness_recovery_per_minute (0.1
-- default) — same physiological recovery per minute whether the
-- vendor is on break or the PC is sleeping. Accuracy guaranteed by
-- the per-actor cursor in last_tiredness_recovery_at.

BEGIN;

-- Safety cap on sleeping_until. Set on /pc/sleep + auto-bed so even a
-- broken recovery sweep + broken checkout doesn't trap the PC
-- forever. 12 hours is more than enough for a max-tiredness recovery
-- (24 / 0.1/min = 4 hours wall-clock), so the cap should rarely fire
-- in practice.
INSERT INTO setting (key, value, description, is_public) VALUES
    ('pc_sleep_max_duration_hours', '12',
     'Sleep safety-cap in wall-clock hours. wakeExpiredSleepers wakes the PC if no other wake condition fires before this. Recovery typically wakes a max-tiredness PC in ~4h, so the cap is a backstop.',
     false)
ON CONFLICT (key) DO NOTHING;

-- Auto-bed tiredness gate. Below this value, an idle lodger in their
-- bedroom subspace is left alone — they're not tired enough to
-- warrant being knocked into bed by a 5-min AFK. 10 is "mild fatigue"
-- on the 0–24 scale.
INSERT INTO setting (key, value, description, is_public) VALUES
    ('pc_idle_sleep_min_tiredness', '10',
     'Minimum actor_need.tiredness for autoBedIdleLodgers to fire on an AFK PC. Set to 0 to revert to the pre-ZBBS-150 always-bed behavior, or to 24 to require max-tiredness.',
     false)
ON CONFLICT (key) DO NOTHING;

COMMIT;
