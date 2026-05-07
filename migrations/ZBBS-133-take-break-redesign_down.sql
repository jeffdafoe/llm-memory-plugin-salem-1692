-- ZBBS-133 down: revert take_break redesign settings.

BEGIN;

DELETE FROM setting WHERE key IN (
    'take_break.eviction_grace_seconds',
    'take_break.eviction_assertive_seconds',
    'take_break.eviction_tiredness_penalty',
    'take_break.tiredness_recovery_per_minute',
    'take_break.cue_red_minutes',
    'take_break.cue_peak_minutes'
);

COMMIT;
