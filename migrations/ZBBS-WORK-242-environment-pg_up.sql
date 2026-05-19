-- ZBBS-WORK-242: Slice 15 — Environment pg-impl.
--
-- Persists v2's sim.WorldEnvironment + sim.Phase to a singleton table
-- and migrates engine-authored state stamps out of the kv `setting`
-- table into typed columns. Seeds default rows for the ~28 new
-- WorldSettings tunables introduced by the v2 rewrite so admins
-- discover the full knob surface in the setting table without having
-- to read the engine source.
--
-- Approach: in-place rename + ADD COLUMN, not a fresh table. v1's
-- world_phase already carries 3 engine-state columns (phase,
-- last_transition_at, last_rotation_at — the latter added by ZBBS-040)
-- so it's already the env+phase singleton in everything but name. The
-- rename makes the table's contents honest; ADD COLUMNs preserve the
-- existing row data without a backfill copy. Village is offline per
-- the engine rewrite, so v1's SELECT ... FROM world_phase breaking is
-- a non-issue.
--
-- State/config partition. Post-cutover convention: `setting` table is
-- CONFIG only (admin-authored, hot-reloadable). Engine-authored state
-- stamps live in world_state typed columns (in the checkpoint write).
-- v1 sneaks state into setting (last_attribute_tick_at + three
-- last_chronicler_* rows) — this migration moves the attribute-tick
-- stamp to a column and deletes the chronicler stamps outright
-- (chronicler is deprecated as v2's tick driver).
--
-- tiredness_critical_threshold_pct → tiredness_critical_threshold. v1
-- stores this single threshold as percent-of-needMax while the sister
-- *_red_threshold settings are absolute 0-24 ints. v2 uses absolute
-- form uniformly. Migration drops the pct row and inserts the abs row
-- with value ceil(24*90/100) = 22.
--
-- AgentTicksPaused is NEW (`agent_ticks_paused`), NOT a polarity flip
-- of v1's `npc_baseline_ticks_enabled` — different knob (baseline
-- cron scheduler vs. global LLM-agent gate). The v1 row deprecates in
-- a separate post-cutover cleanup migration; left untouched here.
--
-- Encoding conventions for new keys: suffix-in-key-name for durations
-- (`_ms` / `_seconds` / `_minutes` / `_hours`) with a scalar int value
-- — matches v1's catalog; no time.ParseDuration syntax anywhere. Range
-- pairs (jitter min/max, visitor stay min/max) are two separate rows,
-- not JSON arrays.

BEGIN;

-- 1. Rename the table. Singleton CHECK constraint name carries forward
-- under its old name in PG; rename it explicitly for clarity. Wrapped
-- in a DO block so the migration survives baseline variance (e.g. a
-- partially-applied up from a recovery scenario where the rename ran
-- but ADD COLUMNs didn't).

ALTER TABLE world_phase RENAME TO world_state;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
          FROM pg_constraint
         WHERE conname = 'world_phase_singleton'
           AND conrelid = 'world_state'::regclass
    ) THEN
        ALTER TABLE world_state RENAME CONSTRAINT world_phase_singleton TO world_state_singleton;
    END IF;
END $$;

-- 2. ADD COLUMNs for env state not yet represented. DEFAULT '' on the
-- prose fields so existing-row backfill is a no-op. last_needs_tick_at
-- is NULL-able (the v1 kv row is also nullable, semantically "never
-- run").

ALTER TABLE world_state ADD COLUMN weather             TEXT NOT NULL DEFAULT '';
ALTER TABLE world_state ADD COLUMN atmosphere          TEXT NOT NULL DEFAULT '';
ALTER TABLE world_state ADD COLUMN last_needs_tick_at  TIMESTAMPTZ;

-- 3. Backfill last_needs_tick_at from the v1 kv row. RFC3339 string in
-- v1; cast straight to timestamptz. NULL stays NULL. After backfill,
-- delete the kv row — the column is now authoritative.
--
-- setting.key is the PK (ZBBS-003) so the subquery returns at most one
-- row. A malformed timestamp causes the cast to abort the migration
-- transaction — intentional: surfacing corrupt scheduler state loudly
-- is preferable to silently dropping it. If a production deployment
-- has a malformed last_attribute_tick_at row, fix it manually before
-- re-running this migration.

UPDATE world_state
   SET last_needs_tick_at = (SELECT value::timestamptz
                               FROM setting
                              WHERE key = 'last_attribute_tick_at'
                                AND value IS NOT NULL)
 WHERE id = 1;

DELETE FROM setting WHERE key = 'last_attribute_tick_at';

-- 4. Clean up deprecated chronicler state rows. Chronicler is no longer
-- the v2 tick driver (atmosphere refresh replaces it for prose; the
-- reactor evaluator replaces it for agent activation). The state stamps
-- aren't restored on rollback — see _down.sql for rollback shape.

DELETE FROM setting WHERE key IN (
    'last_chronicler_phase_fired_at',
    'last_chronicler_fired_phase',
    'last_chronicler_attention_at'
);

-- 5. Rename + convert tiredness_critical from pct to abs (fixes a v1
-- representation inconsistency — see header). Derives the new value
-- from the existing kv row so admin-customized values survive the
-- conversion (e.g. pct=80 → abs=20 = ceil(24*80/100), not the default
-- abs=22). If no pre-existing row exists, the second INSERT seeds the
-- default abs=22 = ceil(24*90/100).

INSERT INTO setting (key, value, description, is_public)
SELECT
    'tiredness_critical_threshold',
    CEIL(24 * value::numeric / 100)::int::text,
    'Critical-tier tiredness threshold as an absolute value (0-24). Lifts the on-shift gate that hides home/inn/tavern from tired-NPC recovery options. Converted from pre-ZBBS-WORK-242 tiredness_critical_threshold_pct via ceil(24 * pct / 100).',
    FALSE
FROM setting
WHERE key = 'tiredness_critical_threshold_pct'
  AND value IS NOT NULL
ON CONFLICT (key) DO NOTHING;

DELETE FROM setting WHERE key = 'tiredness_critical_threshold_pct';

-- Fallback default if no v1 pct row existed (e.g. fresh deploy before
-- ZBBS-172 ever ran). 22 = ceil(24*90/100).
INSERT INTO setting (key, value, description, is_public) VALUES
    ('tiredness_critical_threshold', '22',
     'Critical-tier tiredness threshold as an absolute value (0-24). Lifts the on-shift gate that hides home/inn/tavern from tired-NPC recovery options. Default 22 = ceil(24*90/100), matching the pre-ZBBS-WORK-242 percent-form default of 90.',
     FALSE)
ON CONFLICT (key) DO NOTHING;

-- 6. Catch-up rows for the lazy-seeded zoom settings (engine has
-- code defaults but no migration ever inserted the rows; admin sees
-- them only after the PUT endpoint is hit). Make them discoverable
-- via setting browse.

INSERT INTO setting (key, value, description, is_public) VALUES
    ('world_zoom_min_admin',   '0.1',
     'Minimum zoom floor for admin clients (lower = more zoomed-out allowed). Code default 0.1.',
     FALSE),
    ('world_zoom_min_regular', '0.3',
     'Minimum zoom floor for regular users (lower = more zoomed-out allowed). Code default 0.3.',
     FALSE)
ON CONFLICT (key) DO NOTHING;

-- 7. Seed-all for the ~28 new v2 tunables. Defaults pulled from
-- *Default / default* constants in the engine source; comments
-- condensed from the per-field doc blocks in engine/sim/world.go.

-- Reactor evaluator (9 keys; engine/sim/reactor.go + handlers/pool.go).
INSERT INTO setting (key, value, description, is_public) VALUES
    ('reactor_jitter_min_ms', '1000',
     'Reactor warrant jitter floor in milliseconds. Stamped at warrant time as now+jitter; gives conversational pacing (1-4s default — fires feel like turn-taking, not LLM-speed turbo).',
     FALSE),
    ('reactor_jitter_max_ms', '4000',
     'Reactor warrant jitter ceiling in milliseconds. See reactor_jitter_min_ms.',
     FALSE),
    ('reactor_evaluator_cadence_ms', '250',
     'How often the reactor evaluator runs, in milliseconds. 250ms gives +/-250ms timing precision around the jitter floor — fine for conversational scale.',
     FALSE),
    ('max_warrant_age_seconds', '90',
     'Maximum age of a pending reactor warrant. Cleared on LoadWorld; not currently used at runtime (warrants are ephemeral). Kept for future use if persistence lands.',
     FALSE),
    ('max_warrants_per_actor', '16',
     'Cap on the per-actor pending-warrant list size. When exceeded, oldest entries drop (freshest signals are most relevant). 0 = uncapped.',
     FALSE),
    ('max_reactor_ticks_per_actor_per_minute', '0',
     'Per-actor cap on reactor ticks in a rolling 1-minute window. 0 = disabled (default). Turn on if a noisy environment produces sub-jitter ping-pong loops in practice. Capped actors get their WarrantDueAt pushed to the next allowed time rather than silently skipped.',
     FALSE),
    ('min_reactor_tick_gap_ms', '5000',
     'Always-on per-actor minimum wall-clock gap between reactor ticks, in milliseconds. A warrant coming due inside the gap has its WarrantDueAt pushed to the gap boundary; a Force warrant bypasses it.',
     FALSE),
    ('admission_backoff_ms', '250',
     'How far the evaluator pushes an actor''s WarrantDueAt when tick admission control turns it away (downstream worker pool at capacity), in milliseconds. Roughly matches the evaluator cadence so a deferred warrant is re-examined on the next scan. Warrants stay OPEN — a deferral consumes nothing.',
     FALSE),
    ('tick_worker_count', '1',
     'Number of off-world goroutines in the tick worker pool. Default 1 — a pool > 1 gives nondeterministic cross-actor commit order, so the default must not imply an ordering guarantee the system lacks. The pool derives its bounded job-buffer size from this; backpressure is a feature.',
     FALSE)
ON CONFLICT (key) DO NOTHING;

-- Idle-backstop (2 keys; engine/sim/reactor.go + cascade/idle_backstop.go).
INSERT INTO setting (key, value, description, is_public) VALUES
    ('idle_backstop_threshold_minutes', '30',
     'How long an actor must go without a reactor tick before the idle-backstop sweep stamps a WarrantKindIdleBackstop warrant. Default 30 minutes. Production can tune up; sandbox / dev keeps the default for visible behavior.',
     FALSE),
    ('idle_backstop_sweep_interval_minutes', '5',
     'How often the idle-backstop sweep walks the actor list. Detection latency <= this interval against the threshold; oversample cost is trivial (per-actor field reads on the world goroutine, no allocations).',
     FALSE)
ON CONFLICT (key) DO NOTHING;

-- Atmosphere refresh cascade (1 key; engine/sim/atmosphere.go).
INSERT INTO setting (key, value, description, is_public) VALUES
    ('atmosphere_refresh_interval_hours', '4',
     'Cadence at which the atmosphere refresh cascade fires a salem-generic LLM call to rewrite World.Environment.Atmosphere. Default 4h. Settings-driven from day one so dev/staging can tune it down for testing.',
     FALSE)
ON CONFLICT (key) DO NOTHING;

-- Action-log substrate (2 keys; engine/sim/action_log.go + cascade).
INSERT INTO setting (key, value, description, is_public) VALUES
    ('action_log_retention_hours', '48',
     'How far back the in-memory action log keeps entries. Compaction sweep drops entries with OccurredAt before (now - retention). Default 48h covers atmosphere''s 4h refresh interval with headroom and consolidation''s expected 24h window cleanly.',
     FALSE),
    ('action_log_sweep_interval_hours', '1',
     'How often the action-log compaction sweep fires. Stale entries past retention are still tens of hours old; the sweep cadence just controls how promptly memory is reclaimed.',
     FALSE)
ON CONFLICT (key) DO NOTHING;

-- Visitor cascade (5 keys; engine/sim/visitor.go + cascade).
INSERT INTO setting (key, value, description, is_public) VALUES
    ('visitor_spawn_chance_permille', '0',
     'Per-tick (per-thousand) probability of spawning a new visitor when below the concurrent cap. Default 0 — the feature is no-op until an admin opts in. At visitor_tick_interval_seconds = 60, a value of ~10-30 produces "one visitor per game day on average."',
     FALSE),
    ('visitor_max_concurrent', '2',
     'Cap on simultaneous visitors. The halt-spawn admin dial is visitor_spawn_chance_permille = 0, not a sentinel here.',
     FALSE),
    ('visitor_min_stay_minutes', '240',
     'Minimum visitor stay length in minutes. Concrete stay is a uniform random pull from [min, max] at spawn.',
     FALSE),
    ('visitor_max_stay_minutes', '1440',
     'Maximum visitor stay length in minutes (24h ceiling).',
     FALSE),
    ('visitor_tick_interval_seconds', '60',
     'How often the visitor cascade slice runs its three dispatchers (despawn -> cleanup -> spawn). Default 60s — matches v1''s runServerTickOnce cadence the visitor handlers piggybacked on.',
     FALSE)
ON CONFLICT (key) DO NOTHING;

-- Outdoor scene radius (1 key; engine/sim/world.go).
INSERT INTO setting (key, value, description, is_public) VALUES
    ('default_outdoor_scene_radius', '3',
     'Conversational radius (in king''s-move tiles) used by SceneBoundArea when callers don''t specify one. 0 / unset / negative falls back to 3; values above 10 clamp to 10 at LoadWorld.',
     FALSE)
ON CONFLICT (key) DO NOTHING;

-- Scene-quote substrate (2 keys; engine/sim/scene_quote.go).
INSERT INTO setting (key, value, description, is_public) VALUES
    ('scene_quote_ttl_minutes', '10',
     'How long a freshly minted scene quote stays Active before the aging sweep flips it Expired. Default 10 min — asymmetric (longer) with the pay-ledger pending TTL since a quote is a passive ad rather than a staked offer.',
     FALSE),
    ('scene_quote_sweep_cadence_seconds', '60',
     'How often the scene-quote aging sweep scans World.Quotes for expired entries. +/-60s expiry latency against the 10-min TTL, invisible at gameplay scale.',
     FALSE)
ON CONFLICT (key) DO NOTHING;

-- Pay-ledger substrate (2 keys; engine/sim/pay_ledger.go).
INSERT INTO setting (key, value, description, is_public) VALUES
    ('pay_ledger_ttl_minutes', '3',
     'How long a freshly minted pending pay-ledger entry stays Pending before the aging sweep flips it Expired. Shorter TTL than scene_quote_ttl_minutes — a pending pay offer has the buyer staked into a social moment, which decays faster than a passive quote ad.',
     FALSE),
    ('pay_ledger_sweep_cadence_seconds', '60',
     'How often the pay-ledger aging sweep scans World.PayLedger for expired pending entries. Matches scene_quote_sweep_cadence_seconds so admin tuning sees one mental model.',
     FALSE)
ON CONFLICT (key) DO NOTHING;

-- Order substrate (2 keys; engine/sim/order.go).
INSERT INTO setting (key, value, description, is_public) VALUES
    ('order_ttl_minutes', '10',
     'How long an Order at OrderStateReady sits before the aging sweep flips it OrderStateExpired. Longer than pay_ledger_ttl_minutes since at this stage the buyer has already committed (coins debited) and we want plenty of room for the seller''s reactor to fire and deliver.',
     FALSE),
    ('order_sweep_cadence_seconds', '60',
     'How often the order aging sweep scans World.Orders for expired entries. Matches scene_quote_sweep_cadence_seconds and pay_ledger_sweep_cadence_seconds.',
     FALSE)
ON CONFLICT (key) DO NOTHING;

-- Global agent-tick pause (1 key).
INSERT INTO setting (key, value, description, is_public) VALUES
    ('agent_ticks_paused', 'false',
     'When true, suppresses LLM agent activity globally — reactive NPC ticks and chronicler fires both gated. Worker schedulers, social hours, lamplighter, and rotation continue running. Used to halt agent activity mid-session when a bad loop is being investigated. NOT a polarity-flip of v1''s npc_baseline_ticks_enabled (different knob; deprecate that one separately).',
     FALSE)
ON CONFLICT (key) DO NOTHING;

-- Checkpoint cadence (1 key). No const exists in engine source today;
-- 60s is a moderate starting default. The checkpoint loop itself isn't
-- wired yet (lands at main.go cutover); seeding the row now so admins
-- discover it.
INSERT INTO setting (key, value, description, is_public) VALUES
    ('checkpoint_interval_seconds', '60',
     'How often the checkpoint loop writes the world snapshot to Postgres. Default 60s — moderate balance between data-loss exposure on crash and write load. Tunable post-cutover once the write profile is observed. No consumer until main.go wires the checkpoint loop.',
     FALSE)
ON CONFLICT (key) DO NOTHING;

COMMIT;
