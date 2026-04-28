BEGIN;

DROP INDEX IF EXISTS ix_world_events_occurred_at;
DROP TABLE IF EXISTS world_events;

DROP INDEX IF EXISTS ix_world_environment_set_at;
DROP TABLE IF EXISTS world_environment;

DROP TYPE IF EXISTS event_scope;
DROP TYPE IF EXISTS world_phase;

DELETE FROM setting WHERE key IN (
    'overseer_mood',
    'salem_season',
    'last_chronicler_phase_fired_at',
    'last_chronicler_fired_phase',
    'last_chronicler_attention_at',
    'npc_baseline_ticks_enabled'
);

COMMIT;
