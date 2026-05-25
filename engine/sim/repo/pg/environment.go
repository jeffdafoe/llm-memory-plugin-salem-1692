package pg

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// EnvironmentRepo reads and writes the singleton world_state row (phase
// + env timestamps + atmosphere/weather prose) and parses the kv
// setting table into sim.WorldSettings.
//
// Settings are admin-authored reference state — Load reads them, but
// SaveSnapshot does NOT touch the setting table. The data-partition
// rule: setting kv = config (hot-reloadable via SIGHUP; out of scope
// this slice), world_state singleton = engine-authored state (in the
// checkpoint write). A sibling ReloadSettings method lands when the
// SIGHUP path is wired.
//
// Setting key encoding conventions (matches v1 catalog + new keys
// introduced by ZBBS-WORK-242):
//
//   - Durations use suffix-in-key naming (_ms / _seconds / _minutes /
//     _hours) with a scalar int value. The loader multiplies by the
//     appropriate unit per suffix. No time.ParseDuration syntax.
//   - Bools are stored as 'true' / 'false' strings.
//   - Floats are stored as the natural decimal representation
//     ('0.1' / '0.3' etc).
//   - Range pairs are two separate rows, not JSON arrays.
//
// Missing rows fall back to the *Default / default* constants in the
// engine source. Malformed values log a warning and fall back —
// permissive-with-fallback is the right posture for an admin-authored
// table where a typo shouldn't prevent boot. Hard schema errors
// (world_state row missing, NULL non-nullable columns) still surface
// loudly.
type EnvironmentRepo struct {
	pool Pool
}

// NewEnvironmentRepo constructs an EnvironmentRepo against the given
// pool. Normal wiring path is pg.NewRepository.
func NewEnvironmentRepo(pool Pool) *EnvironmentRepo {
	return &EnvironmentRepo{pool: pool}
}

// loadWorldStateSQL reads the singleton row. id=1 is enforced by the
// world_state_singleton CHECK constraint.
const loadWorldStateSQL = `
SELECT phase, last_transition_at, last_rotation_at, weather, atmosphere, last_needs_tick_at
  FROM world_state
 WHERE id = 1`

// loadSettingsSQL reads every setting row. Caller-side filtering is
// fine — the table is small (well under 1000 rows even at full
// production seed).
const loadSettingsSQL = `SELECT key, value FROM setting WHERE value IS NOT NULL`

// upsertWorldStateSQL writes the singleton. Plain UPSERT — no gen
// counter, the row is one-of-one by CHECK constraint.
const upsertWorldStateSQL = `
INSERT INTO world_state (
    id, phase, last_transition_at, last_rotation_at,
    weather, atmosphere, last_needs_tick_at
) VALUES (
    1, $1, $2, $3, $4, $5, $6
)
ON CONFLICT (id) DO UPDATE SET
    phase              = EXCLUDED.phase,
    last_transition_at = EXCLUDED.last_transition_at,
    last_rotation_at   = EXCLUDED.last_rotation_at,
    weather            = EXCLUDED.weather,
    atmosphere         = EXCLUDED.atmosphere,
    last_needs_tick_at = EXCLUDED.last_needs_tick_at`

// Load reads the world_state singleton + every setting row, returning
// a fully populated (env, phase, settings) triple. Missing setting rows
// fall back to *Default constants. The singleton row is required —
// pg.errNoWorldState surfaces if no row exists at id=1 (should never
// happen post-migration; defensive against fresh deploys without the
// ZBBS-038 seed).
//
// Runs against the pool directly (no Tx) — read-only restart path.
// Same posture as other repos' LoadAll.
func (r *EnvironmentRepo) Load(ctx context.Context) (sim.WorldEnvironment, sim.Phase, sim.WorldSettings, error) {
	env, phase, err := r.loadWorldState(ctx)
	if err != nil {
		return sim.WorldEnvironment{}, sim.Phase(""), sim.WorldSettings{}, err
	}
	values, err := r.loadSettings(ctx)
	if err != nil {
		return sim.WorldEnvironment{}, sim.Phase(""), sim.WorldSettings{}, err
	}
	settings := buildSettings(values)
	return env, phase, settings, nil
}

// loadWorldState reads the singleton row into a WorldEnvironment +
// Phase. The phase column has a CHECK ('day' | 'night') so any
// well-formed row decodes cleanly into sim.Phase.
func (r *EnvironmentRepo) loadWorldState(ctx context.Context) (sim.WorldEnvironment, sim.Phase, error) {
	var (
		phase               string
		lastTransitionAt    time.Time
		lastRotationAt      time.Time
		weather, atmosphere string
		lastNeedsTickAt     *time.Time
	)
	err := r.pool.QueryRow(ctx, loadWorldStateSQL).Scan(
		&phase, &lastTransitionAt, &lastRotationAt,
		&weather, &atmosphere, &lastNeedsTickAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return sim.WorldEnvironment{}, sim.Phase(""),
				fmt.Errorf("pg environment Load: world_state row missing (expected id=1 — seeded by ZBBS-038 / renamed by ZBBS-WORK-242): %w", err)
		}
		return sim.WorldEnvironment{}, sim.Phase(""),
			fmt.Errorf("pg environment Load: world_state query: %w", err)
	}
	env := sim.WorldEnvironment{
		LastTransitionAt: lastTransitionAt,
		LastRotationAt:   lastRotationAt,
		Weather:          weather,
		Atmosphere:       atmosphere,
	}
	if lastNeedsTickAt != nil {
		env.LastNeedsTickAt = *lastNeedsTickAt
	}
	// Now and LastAtmosphereRefreshAt are restart-lossy / live-clock —
	// not stored. LoadWorld stamps LoadedAt separately.
	return env, sim.Phase(phase), nil
}

// loadSettings reads every non-NULL setting row into a key→value map.
// Drives the per-field parse helpers below.
func (r *EnvironmentRepo) loadSettings(ctx context.Context) (map[string]string, error) {
	rows, err := r.pool.Query(ctx, loadSettingsSQL)
	if err != nil {
		return nil, fmt.Errorf("pg environment Load: setting query: %w", err)
	}
	defer rows.Close()
	values := make(map[string]string)
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, fmt.Errorf("pg environment Load: setting scan: %w", err)
		}
		values[k] = v
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pg environment Load: setting iter: %w", err)
	}
	return values, nil
}

// buildSettings populates a sim.WorldSettings from the raw kv map.
// Every field has a default fallback; missing or malformed rows log
// a warning (via the helper) and use the default.
//
// Field ordering matches sim.WorldSettings declaration order so a
// reader can audit the loader against the struct top-to-bottom.
func buildSettings(values map[string]string) sim.WorldSettings {
	s := sim.WorldSettings{}

	s.CheckpointInterval = parseDurationSetting(values, "checkpoint_interval_seconds", 60*time.Second)

	s.DawnTime = parseStringSetting(values, "world_dawn_time", sim.DefaultDawn)
	s.DuskTime = parseStringSetting(values, "world_dusk_time", sim.DefaultDusk)
	s.RotationTime = parseStringSetting(values, "world_rotation_time", sim.DefaultRotationTime)
	s.Timezone = parseStringSetting(values, "world_timezone", sim.DefaultTimezone)
	if loc, err := time.LoadLocation(s.Timezone); err == nil {
		s.Location = loc
	} else {
		log.Printf("pg environment: invalid world_timezone=%q (%v) — falling back to %s",
			s.Timezone, err, sim.DefaultTimezone)
		s.Timezone = sim.DefaultTimezone
		s.Location, _ = time.LoadLocation(sim.DefaultTimezone)
	}

	s.ZoomMinAdmin = parseFloatSetting(values, "world_zoom_min_admin", sim.DefaultZoomMinAdmin)
	s.ZoomMinRegular = parseFloatSetting(values, "world_zoom_min_regular", sim.DefaultZoomMinRegular)

	s.AgentTicksPaused = parseBoolSetting(values, "agent_ticks_paused", false)

	s.LodgingCheckInHour = parseIntSetting(values, "lodging_check_in_hour", 15)
	s.LodgingCheckOutHour = parseIntSetting(values, "lodging_check_out_hour", 11)
	s.LodgingDefaultWeeklyRate = parseIntSetting(values, "lodging_default_weekly_rate", 28)
	s.ShiftLatenessWindowMinutes = parseIntSetting(values, "shift_lateness_window_minutes", sim.DefaultShiftLatenessWindowMinutes)
	s.NPCSleepMaxDurationHours = parseIntSetting(values, "npc_sleep_max_duration_hours", sim.DefaultNPCSleepMaxDurationHours)

	s.NeedsTickAmount = parseIntSetting(values, "attribute_tick_amount", sim.DefaultNeedsTickAmount)
	s.NeedThresholds = loadNeedThresholds(values)
	s.TirednessCriticalThreshold = parseIntSetting(values, "tiredness_critical_threshold",
		(sim.NeedMax*sim.DefaultTirednessCriticalThresholdPct+99)/100)
	s.MovementFatiguePerTileX100 = parseIntSetting(values, "movement_fatigue_per_tile_x100", sim.DefaultMovementFatiguePerTileX100)
	s.TirednessRecoveryPerMinuteX100 = parseIntSetting(values, "tiredness_recovery_per_minute_x100", sim.DefaultTirednessRecoveryPerMinuteX100)
	s.RestockReorderPct = parseIntSetting(values, "restock_reorder_pct", sim.DefaultRestockReorderPct)

	// Reactor evaluator tunables.
	s.ReactorJitterMin = parseDurationSetting(values, "reactor_jitter_min_ms", 1*time.Second)
	s.ReactorJitterMax = parseDurationSetting(values, "reactor_jitter_max_ms", 4*time.Second)
	s.ReactorEvaluatorCadence = parseDurationSetting(values, "reactor_evaluator_cadence_ms", 250*time.Millisecond)
	s.MaxWarrantAge = parseDurationSetting(values, "max_warrant_age_seconds", 90*time.Second)
	s.MaxReactorTicksPerActorPerMinute = parseIntSetting(values, "max_reactor_ticks_per_actor_per_minute", 0)
	s.MaxWarrantsPerActor = parseIntSetting(values, "max_warrants_per_actor", 16)
	s.MinReactorTickGap = parseDurationSetting(values, "min_reactor_tick_gap_ms", 5*time.Second)
	s.AdmissionBackoff = parseDurationSetting(values, "admission_backoff_ms", 250*time.Millisecond)
	s.TickWorkerCount = parseIntSetting(values, "tick_worker_count", 1)

	// Idle backstop.
	s.IdleBackstopThreshold = parseDurationSetting(values, "idle_backstop_threshold_minutes", 30*time.Minute)
	s.IdleBackstopSweepInterval = parseDurationSetting(values, "idle_backstop_sweep_interval_minutes", 5*time.Minute)

	// Atmosphere refresh cascade.
	s.AtmosphereRefreshInterval = parseDurationSetting(values, "atmosphere_refresh_interval_hours", 4*time.Hour)

	// Action-log substrate.
	s.ActionLogRetention = parseDurationSetting(values, "action_log_retention_hours", 48*time.Hour)
	s.ActionLogSweepInterval = parseDurationSetting(values, "action_log_sweep_interval_hours", 1*time.Hour)

	// Visitor cascade.
	s.VisitorSpawnChancePermille = parseIntSetting(values, "visitor_spawn_chance_permille", 0)
	s.VisitorMaxConcurrent = parseIntSetting(values, "visitor_max_concurrent", 2)
	s.VisitorMinStayMinutes = parseIntSetting(values, "visitor_min_stay_minutes", 240)
	s.VisitorMaxStayMinutes = parseIntSetting(values, "visitor_max_stay_minutes", 1440)
	s.VisitorTickInterval = parseDurationSetting(values, "visitor_tick_interval_seconds", 60*time.Second)

	// Businessowner cooldowns.
	s.BusinessownerGreetCooldownMinutes = parseIntSetting(values, "businessowner_greet_cooldown_minutes",
		sim.DefaultBusinessownerGreetCooldownMinutes)
	s.BusinessownerFarewellCooldownMinutes = parseIntSetting(values, "businessowner_farewell_cooldown_minutes",
		sim.DefaultBusinessownerFarewellCooldownMinutes)

	// Outdoor scene radius.
	s.DefaultOutdoorSceneRadius = parseIntSetting(values, "default_outdoor_scene_radius", sim.DefaultOutdoorSceneRadiusValue)

	// Scene quote.
	s.SceneQuoteTTL = parseDurationSetting(values, "scene_quote_ttl_minutes", 10*time.Minute)
	s.SceneQuoteSweepCadence = parseDurationSetting(values, "scene_quote_sweep_cadence_seconds", 60*time.Second)

	// Pay ledger.
	s.PayLedgerTTL = parseDurationSetting(values, "pay_ledger_ttl_minutes", 3*time.Minute)
	s.PayLedgerSweepCadence = parseDurationSetting(values, "pay_ledger_sweep_cadence_seconds", 60*time.Second)

	// Order.
	s.OrderTTL = parseDurationSetting(values, "order_ttl_minutes", 10*time.Minute)
	s.OrderSweepCadence = parseDurationSetting(values, "order_sweep_cadence_seconds", 60*time.Second)

	// PC presence staleness (ZBBS-WORK-326).
	s.PCPresenceStaleAfter = parseDurationSetting(values, "pc_presence_stale_seconds", sim.DefaultPCPresenceStaleAfter)

	return s
}

// loadNeedThresholds walks the sim.Needs registry and pulls each
// need's red threshold from the kv map, falling back to the registry's
// DefaultThreshold. Drives off the registry so adding a new need
// slot doesn't require touching this loader.
func loadNeedThresholds(values map[string]string) sim.NeedThresholds {
	out := make(sim.NeedThresholds, len(sim.Needs))
	for _, n := range sim.Needs {
		out[n.Key] = parseIntSetting(values, n.ThresholdSettingKey, n.DefaultThreshold)
	}
	return out
}

// parseStringSetting returns the kv value if present and non-empty;
// otherwise def. Empty strings count as "not set" since we already
// filter NULL at SQL.
func parseStringSetting(values map[string]string, key, def string) string {
	v, ok := values[key]
	if !ok || v == "" {
		return def
	}
	return v
}

// parseIntSetting returns the kv value parsed as an int. Missing or
// malformed rows log a warning and use def.
func parseIntSetting(values map[string]string, key string, def int) int {
	raw, ok := values[key]
	if !ok {
		return def
	}
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		log.Printf("pg environment: setting %q=%q is not a valid int (%v) — falling back to %d",
			key, raw, err, def)
		return def
	}
	return n
}

// parseFloatSetting returns the kv value parsed as a float64. Missing
// or malformed rows log a warning and use def.
func parseFloatSetting(values map[string]string, key string, def float64) float64 {
	raw, ok := values[key]
	if !ok {
		return def
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil {
		log.Printf("pg environment: setting %q=%q is not a valid float (%v) — falling back to %v",
			key, raw, err, def)
		return def
	}
	return f
}

// parseBoolSetting returns the kv value parsed as a bool. Accepts
// 'true' / 'false' (case-insensitive). Anything else logs a warning
// and uses def.
func parseBoolSetting(values map[string]string, key string, def bool) bool {
	raw, ok := values[key]
	if !ok {
		return def
	}
	b, err := strconv.ParseBool(strings.TrimSpace(raw))
	if err != nil {
		log.Printf("pg environment: setting %q=%q is not a valid bool (%v) — falling back to %v",
			key, raw, err, def)
		return def
	}
	return b
}

// parseDurationSetting reads a scalar-int setting and multiplies by
// the unit implied by the key's suffix (_ms / _seconds / _minutes /
// _hours). Missing rows, malformed values, unrecognized suffixes,
// negative values, and overflowing multiplications all log a warning
// and return def.
//
// Negative values are universally invalid for cadences/TTLs/backoffs
// (would produce tight loops or immediate-expiry behavior). Zero IS
// valid per-key — many fields use zero to mean "disabled" — so the
// zero floor stays open here.
//
// Overflow guard prevents an admin typo like 'atmosphere_refresh_
// interval_hours = 99999999' from wrapping time.Duration negative.
//
// Unrecognized suffix is a programming error (the caller passed a key
// without one of the four supported suffixes); separate diagnostic
// path to make the cause obvious.
func parseDurationSetting(values map[string]string, key string, def time.Duration) time.Duration {
	unit, ok := durationUnitForKey(key)
	if !ok {
		log.Printf("pg environment: setting %q has no recognized duration suffix (expected _ms / _seconds / _minutes / _hours) — falling back to %v",
			key, def)
		return def
	}
	raw, present := values[key]
	if !present {
		return def
	}
	n, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil {
		log.Printf("pg environment: setting %q=%q is not a valid int (%v) — falling back to %v",
			key, raw, err, def)
		return def
	}
	if n < 0 {
		log.Printf("pg environment: setting %q=%q is negative — falling back to %v",
			key, raw, def)
		return def
	}
	if n > math.MaxInt64/int64(unit) {
		log.Printf("pg environment: setting %q=%q overflows time.Duration when multiplied by %v — falling back to %v",
			key, raw, unit, def)
		return def
	}
	return time.Duration(n) * unit
}

// durationUnitForKey returns the time unit implied by the key's
// suffix. Suffix-driven so adding a new duration setting doesn't
// require any change here as long as the key name follows the
// convention.
func durationUnitForKey(key string) (time.Duration, bool) {
	switch {
	case strings.HasSuffix(key, "_ms"):
		return time.Millisecond, true
	case strings.HasSuffix(key, "_seconds"):
		return time.Second, true
	case strings.HasSuffix(key, "_minutes"):
		return time.Minute, true
	case strings.HasSuffix(key, "_hours"):
		return time.Hour, true
	default:
		return 0, false
	}
}

// SaveSnapshot writes the world_state singleton inside the caller's
// checkpoint Tx. Plain UPSERT on id=1; no gen counter (singleton).
// Settings are NOT touched here — they're reference state, reloaded
// via a separate SIGHUP path (out of scope this slice).
//
// LastAtmosphereRefreshAt and Now are restart-lossy / live-clock and
// not persisted (see header).
//
// last_needs_tick_at is nullable in SQL; a zero env.LastNeedsTickAt
// time.Time encodes as SQL NULL ("never run").
func (r *EnvironmentRepo) SaveSnapshot(ctx context.Context, tx sim.Tx, env sim.WorldEnvironment, phase sim.Phase) error {
	if tx == nil {
		return fmt.Errorf("pg environment SaveSnapshot: nil tx")
	}
	if phase != sim.PhaseDay && phase != sim.PhaseNight {
		return fmt.Errorf("pg environment SaveSnapshot: invalid phase %q (expected day | night)", phase)
	}
	// Substrate-boundary guard: both required timestamps must be set.
	// Zero time.Time encodes as PG year-0001 (not caught by NOT NULL),
	// which would silently corrupt the scheduler gates the engine
	// relies on. LoadWorld seeds these from world_state at startup; a
	// zero value here indicates upstream forgot to copy through.
	// LastNeedsTickAt zero IS legitimate (= "never run yet" = SQL NULL).
	if env.LastTransitionAt.IsZero() {
		return fmt.Errorf("pg environment SaveSnapshot: zero LastTransitionAt (scheduler state would corrupt to year 0001)")
	}
	if env.LastRotationAt.IsZero() {
		return fmt.Errorf("pg environment SaveSnapshot: zero LastRotationAt (scheduler state would corrupt to year 0001)")
	}
	var lastNeedsArg any
	if !env.LastNeedsTickAt.IsZero() {
		lastNeedsArg = env.LastNeedsTickAt
	}
	if _, err := tx.Exec(ctx, upsertWorldStateSQL,
		string(phase),        // $1 phase
		env.LastTransitionAt, // $2 last_transition_at
		env.LastRotationAt,   // $3 last_rotation_at
		env.Weather,          // $4 weather
		env.Atmosphere,       // $5 atmosphere
		lastNeedsArg,         // $6 last_needs_tick_at (nullable)
	); err != nil {
		return fmt.Errorf("pg environment SaveSnapshot: upsert: %w", err)
	}
	return nil
}
