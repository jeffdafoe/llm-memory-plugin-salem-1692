-- ZBBS-133: take_break redesign — close-shop-with-vendor-inside model.
--
-- Settings rows for the redesign locked at `shared/tasks/take-break-
-- redesign/design`. Six knobs:
--
--   take_break.eviction_grace_seconds (default 180):
--     Phase 1 wait. After the vendor's spoken excuse is broadcast,
--     customers have this long to leave on their own before the
--     assertive-ask phase fires.
--
--   take_break.eviction_assertive_seconds (default 300):
--     Phase 2 wait. After the assertive-ask LLM call, customers have
--     this long before force-eject.
--
--   take_break.eviction_tiredness_penalty (default 3):
--     +N tiredness applied to anyone force-ejected, capped at 24.
--
--   take_break.tiredness_recovery_per_minute (default 0.1):
--     Rate at which tiredness drops while an actor is on break
--     (break_until > NOW()). 6× the accrual rate so 30-min break = -3,
--     60-min = -6, 4h default = full reset (-24, capped). Branched in
--     needs.go's accrual path: when on-break, apply a delta of
--     -tiredness_recovery_per_minute × elapsedMin instead of the
--     positive accrual delta.
--
--   take_break.cue_red_minutes (default 30):
--     Suggested duration in the red-tier "weary" perception cue.
--
--   take_break.cue_peak_minutes (default 60):
--     Suggested duration in the peak-tier "exhausted" perception cue.
--
-- Real LLM call for the phase-2 assertive-ask (Jeff's call this
-- session): vendor's persona shows through in their own voice. One
-- LLM call per take_break that has lingerers. Cost is bursty but
-- bounded.
--
-- Forced co-located ticks on every non-exempt actor inside when
-- take_break fires (Jeff's call this session): bypasses the cost
-- guard via the same force=true mechanism as ZBBS-126's post-pay
-- reactor tick. Customers tick within seconds of the announcement
-- and get the full eviction_grace_seconds window to actually leave.

BEGIN;

INSERT INTO setting (key, value, description, is_public) VALUES
    ('take_break.eviction_grace_seconds',          '180',  'Phase 1 wait (seconds): customers have this long to leave on their own after the vendor announces the break before the assertive-ask phase fires', false),
    ('take_break.eviction_assertive_seconds',      '300',  'Phase 2 wait (seconds): time between the assertive-ask LLM call and force-eject', false),
    ('take_break.eviction_tiredness_penalty',      '3',    'Tiredness +N applied to actors force-ejected at phase 3, capped at 24', false),
    ('take_break.tiredness_recovery_per_minute',   '0.1',  'Rate (per minute) tiredness drops while an actor is on break. 6× the accrual rate; 4h break = full reset', false),
    ('take_break.cue_red_minutes',                 '30',   'Suggested duration in the red-tier weary perception cue for vendors with the take_break tool', false),
    ('take_break.cue_peak_minutes',                '60',   'Suggested duration in the peak-tier exhausted perception cue for vendors with the take_break tool', false);

COMMIT;
