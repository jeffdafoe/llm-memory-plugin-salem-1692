-- Rollback ZBBS-WORK-242 — Environment pg-impl.
--
-- Reverses the rename, drops the new env columns, restores the
-- engine-state kv rows (chronicler stamps + last_attribute_tick_at),
-- reverses the tiredness pct/abs swap, and deletes the seeded v2
-- tunables. The zoom catch-up rows are NOT deleted on rollback — the
-- code defaults match the seeded values anyway, so leaving them is a
-- no-op for the engine but preserves any admin edits.

BEGIN;

-- 1. Delete the ~28 v2 tunables.
DELETE FROM setting WHERE key IN (
    'reactor_jitter_min_ms',
    'reactor_jitter_max_ms',
    'reactor_evaluator_cadence_ms',
    'max_warrant_age_seconds',
    'max_warrants_per_actor',
    'max_reactor_ticks_per_actor_per_minute',
    'min_reactor_tick_gap_ms',
    'admission_backoff_ms',
    'tick_worker_count',
    'idle_backstop_threshold_minutes',
    'idle_backstop_sweep_interval_minutes',
    'atmosphere_refresh_interval_hours',
    'action_log_retention_hours',
    'action_log_sweep_interval_hours',
    'visitor_spawn_chance_permille',
    'visitor_max_concurrent',
    'visitor_min_stay_minutes',
    'visitor_max_stay_minutes',
    'visitor_tick_interval_seconds',
    'default_outdoor_scene_radius',
    'scene_quote_ttl_minutes',
    'scene_quote_sweep_cadence_seconds',
    'pay_ledger_ttl_minutes',
    'pay_ledger_sweep_cadence_seconds',
    'order_ttl_minutes',
    'order_sweep_cadence_seconds',
    'agent_ticks_paused',
    'checkpoint_interval_seconds'
);

-- 2. Reverse the tiredness pct/abs swap.
DELETE FROM setting WHERE key = 'tiredness_critical_threshold';

INSERT INTO setting (key, value, description, is_public) VALUES
    ('tiredness_critical_threshold_pct', '90',
     'Critical-tier tiredness threshold as percent of needMax. Engine computes the absolute as ceil(needMax * pct / 100). Lifts the on-shift gate that hides home/inn/tavern from tired-NPC recovery options. Default 90.',
     FALSE)
ON CONFLICT (key) DO NOTHING;

-- 3. Restore the deprecated chronicler state rows with NULL values.
-- Their original descriptions weren't preserved in the up migration —
-- restore with the v1 forms used by ZBBS-081.
INSERT INTO setting (key, value) VALUES
    ('last_chronicler_phase_fired_at', NULL),
    ('last_chronicler_fired_phase',    NULL),
    ('last_chronicler_attention_at',   NULL)
ON CONFLICT (key) DO NOTHING;

-- 4. Restore last_attribute_tick_at from the typed column. Format the
-- timestamptz back to RFC3339 to match v1's storage form (or NULL).
INSERT INTO setting (key, value, description) VALUES
    ('last_attribute_tick_at',
     (SELECT to_char(last_needs_tick_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"')
        FROM world_state WHERE id = 1),
     'State row. RFC3339 timestamp of the most recent attribute tick, truncated to the hour. NULL = never run. Replaces last_attribute_tick_hour (int 0-23) which lost day-wrap information.')
ON CONFLICT (key) DO NOTHING;

-- 5. Drop the new env columns. Reverse-order of the up ADD COLUMN block.
ALTER TABLE world_state DROP COLUMN IF EXISTS last_needs_tick_at;
ALTER TABLE world_state DROP COLUMN IF EXISTS atmosphere;
ALTER TABLE world_state DROP COLUMN IF EXISTS weather;

-- 6. Rename the singleton constraint back, then the table itself.
ALTER TABLE world_state RENAME CONSTRAINT world_state_singleton TO world_phase_singleton;
ALTER TABLE world_state RENAME TO world_phase;

COMMIT;
