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

// upsertSettingSQL writes one kv setting row. Used by SaveMutableSettings for
// the runtime-tunable subset only (NOT a full settings replace). value is text;
// callers format floats/bools to the same string shape the loader parses.
const upsertSettingSQL = `
INSERT INTO setting (key, value) VALUES ($1, $2)
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`

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

	s.LodgingCheckOutHour = parseIntSetting(values, "lodging_check_out_hour", 11)
	s.LodgingBedtimeHour = parseIntSetting(values, "lodging_bedtime_hour", sim.DefaultLodgingBedtimeHour)
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

	// Stall wear & repair (LLM-118).
	s.StallWearPerCoin = parseIntSetting(values, "stall_wear_per_coin", sim.DefaultStallWearPerCoin)
	s.StallWearRepairThreshold = parseIntSetting(values, "stall_wear_repair_threshold", sim.DefaultStallWearRepairThreshold)
	s.StallWearDegradeThreshold = parseIntSetting(values, "stall_wear_degrade_threshold", sim.DefaultStallWearDegradeThreshold)
	s.StallNailsPerRepair = parseIntSetting(values, "stall_nails_per_repair", sim.DefaultStallNailsPerRepair)
	s.StallRepairDurationSeconds = parseIntSetting(values, "stall_repair_duration_seconds", sim.DefaultStallRepairDurationSeconds)
	s.StallDegradedProducePct = parseIntSetting(values, "stall_degraded_produce_pct", sim.DefaultStallDegradedProducePct)

	// Farm upkeep wealth tax (LLM-215).
	s.FarmUpkeepFloor = parseIntSetting(values, "farm_upkeep_floor", sim.DefaultFarmUpkeepFloor)
	s.FarmUpkeepCoinsPerShovel = parseIntSetting(values, "farm_upkeep_coins_per_shovel", sim.DefaultFarmUpkeepCoinsPerShovel)

	// Cold exposure + hearth (LLM-412). Every cold knob is a per-minute rate, a
	// multiplier, or a percentage — all of which must be >= 0 (a negative recovery
	// rate would FLIP recovery into accrual, `return -setting` going positive; a
	// negative accrual/cap/multiplier/sap is likewise nonsense). 0 is IN RANGE for
	// each — its meaning varies (no accrual / a coat is full relief / no sap / the
	// night multiplier's `> 0` guard selects the plain daytime rate) but is never
	// itself invalid — so the floor is 0, not the default. clampNonNegSetting clamps
	// a negative to 0 and records a SettingWarning (LLM-439) so the umbilical
	// /settings surface shows the bad config rather than silently correcting it —
	// keeping the always-live village booting on a fat-fingered deploy. The runtime
	// guards in coldRatePerMinuteX100 (the accrual/garment `g >= 0` gates and the
	// recovery-branch max(0, …) floors) stay as defense in depth.
	s.ColdStormOutdoorsPerMinuteX100 = clampNonNegSetting(values, "cold_storm_outdoors_per_minute_x100", sim.DefaultColdStormOutdoorsPerMinuteX100, &s.SettingWarnings)
	s.ColdStormIndoorsPerMinuteX100 = clampNonNegSetting(values, "cold_storm_indoors_per_minute_x100", sim.DefaultColdStormIndoorsPerMinuteX100, &s.SettingWarnings)
	s.ColdWarmGarmentPerMinuteX100 = clampNonNegSetting(values, "cold_warm_garment_per_minute_x100", sim.DefaultColdWarmGarmentPerMinuteX100, &s.SettingWarnings)
	s.ColdThreadbareGarmentPerMinuteX100 = clampNonNegSetting(values, "cold_threadbare_garment_per_minute_x100", sim.DefaultColdThreadbareGarmentPerMinuteX100, &s.SettingWarnings)
	s.ColdNightMultiplierX100 = clampNonNegSetting(values, "cold_night_multiplier_x100", sim.DefaultColdNightMultiplierX100, &s.SettingWarnings)
	s.ColdWarmRecoveryPerMinuteX100 = clampNonNegSetting(values, "cold_warm_recovery_per_minute_x100", sim.DefaultColdWarmRecoveryPerMinuteX100, &s.SettingWarnings)
	s.ColdClearRecoveryPerMinuteX100 = clampNonNegSetting(values, "cold_clear_recovery_per_minute_x100", sim.DefaultColdClearRecoveryPerMinuteX100, &s.SettingWarnings)
	s.ColdProduceSapPct = clampNonNegSetting(values, "cold_produce_sap_pct", sim.DefaultColdProduceSapPct, &s.SettingWarnings)
	s.HearthBurnMinutesPerWood = parseIntSetting(values, "hearth_burn_minutes_per_wood", sim.DefaultHearthBurnMinutesPerWood)
	s.HearthMaxBankMinutes = parseIntSetting(values, "hearth_max_bank_minutes", sim.DefaultHearthMaxBankMinutes)
	s.HearthLowMinutes = parseIntSetting(values, "hearth_low_minutes", sim.DefaultHearthLowMinutes)
	s.StokeWoodPerStoke = parseIntSetting(values, "stoke_wood_per_stoke", sim.DefaultStokeWoodPerStoke)
	s.StokeDurationSeconds = parseIntSetting(values, "stoke_duration_seconds", sim.DefaultStokeDurationSeconds)

	// Garment wear (LLM-422).
	s.GarmentWearPerMinute = parseIntSetting(values, "garment_wear_per_minute", sim.DefaultGarmentWearPerMinute)
	s.GarmentThreadbareFractionX100 = parseIntSetting(values, "garment_threadbare_fraction_x100", sim.DefaultGarmentThreadbareFractionX100)

	// Reactor evaluator tunables.
	s.ReactorJitterMin = parseDurationSetting(values, "reactor_jitter_min_ms", 1*time.Second)
	s.ReactorJitterMax = parseDurationSetting(values, "reactor_jitter_max_ms", 4*time.Second)
	s.ReactorEvaluatorCadence = parseDurationSetting(values, "reactor_evaluator_cadence_ms", 250*time.Millisecond)
	s.MaxWarrantAge = parseDurationSetting(values, "max_warrant_age_seconds", 90*time.Second)
	s.MaxReactorTicksPerActorPerMinute = parseIntSetting(values, "max_reactor_ticks_per_actor_per_minute", 0)
	s.MaxWarrantsPerActor = parseIntSetting(values, "max_warrants_per_actor", 16)
	s.MinReactorTickGap = parseDurationSetting(values, "min_reactor_tick_gap_ms", 5*time.Second)
	s.LaborReplyCadence = parseDurationSetting(values, "labor_reply_cadence_ms", 3*time.Minute)
	s.AdmissionBackoff = parseDurationSetting(values, "admission_backoff_ms", 250*time.Millisecond)
	s.TickWorkerCount = parseIntSetting(values, "tick_worker_count", 1)

	// Degeneracy observer (LLM-94, engine/sim/degeneracy.go). OFF by default —
	// set degeneracy_thin_after_ticks to a positive value to enable it. The
	// three Stage-2 sub-knobs fall back to safe defaults (20 ticks / 15m / 5m,
	// owned by the resolvers in degeneracy.go) when left 0.
	s.DegeneracyThinAfterTicks = parseIntSetting(values, "degeneracy_thin_after_ticks", 0)
	s.DegeneracyThrottleAfterTicks = parseIntSetting(values, "degeneracy_throttle_after_ticks", 0)
	s.DegeneracyThrottleMinDuration = parseDurationSetting(values, "degeneracy_throttle_min_duration_minutes", 0)
	s.DegeneracyThrottleBackoff = parseDurationSetting(values, "degeneracy_throttle_backoff_minutes", 0)
	// Oscillation arm (LLM-124). All fall back to safe defaults (8 / 3 / 2,
	// owned by the resolvers in degeneracy.go) when left 0; active only while
	// the observer above is enabled.
	s.DegeneracyOscillationWindow = parseIntSetting(values, "degeneracy_oscillation_window", 0)
	s.DegeneracyOscillationMinTransitions = parseIntSetting(values, "degeneracy_oscillation_min_transitions", 0)
	s.DegeneracyOscillationMaxDistinct = parseIntSetting(values, "degeneracy_oscillation_max_distinct", 0)

	// Staleness decay for level-triggered warrants (LLM-233,
	// engine/sim/stale_wake.go). ON by default — the gate keys on an exact
	// situation-fingerprint equality (not a heuristic) and any real change or
	// salient warrant lifts it instantly. Set stale_wake_decay_base_seconds
	// to 0 to disable. The cap falls back to 30m (owned by the resolver in
	// stale_wake.go) when left 0.
	s.StaleWakeDecayBase = parseDurationSetting(values, "stale_wake_decay_base_seconds", time.Minute)
	s.StaleWakeDecayCap = parseDurationSetting(values, "stale_wake_decay_cap_minutes", 0)

	// Idle backstop.
	s.IdleBackstopThreshold = parseDurationSetting(values, "idle_backstop_threshold_minutes", 30*time.Minute)
	s.IdleBackstopSweepInterval = parseDurationSetting(values, "idle_backstop_sweep_interval_minutes", 5*time.Minute)

	// Red-need backstop (ZBBS-HOME-363). Base is the floor re-warrant gap
	// for a red-need idle actor; the per-actor backoff doubles it each
	// no-progress sweep up to the max (= idle-backstop rate, bounding stuck
	// cost). Sweep interval sets detection latency for a newly-red actor.
	s.RedNeedBackstopBaseDelay = parseDurationSetting(values, "red_need_backstop_base_delay_seconds", 90*time.Second)
	s.RedNeedBackstopMaxDelay = parseDurationSetting(values, "red_need_backstop_max_delay_minutes", 30*time.Minute)
	s.RedNeedBackstopSweepInterval = parseDurationSetting(values, "red_need_backstop_sweep_interval_seconds", 30*time.Second)

	// Atmosphere refresh cascade.
	s.AtmosphereRefreshInterval = parseDurationSetting(values, "atmosphere_refresh_interval_hours", 4*time.Hour)

	// Storm weather cascade (LLM-117). Minute-granularity keys so dev /
	// staging can tune the auto-cadence right down for testing without a
	// rebuild (the umbilical /weather force-path is the instant test tool;
	// these govern the unattended cadence).
	s.StormInterval = parseDurationSetting(values, "storm_interval_minutes", 180*time.Minute)
	s.StormDuration = parseDurationSetting(values, "storm_duration_minutes", 15*time.Minute)

	// Action-log substrate.
	s.ActionLogRetention = parseDurationSetting(values, "action_log_retention_hours", 48*time.Hour)
	s.ActionLogSweepInterval = parseDurationSetting(values, "action_log_sweep_interval_hours", 1*time.Hour)

	// Visitor cascade.
	s.VisitorSpawnChancePermille = parseIntSetting(values, "visitor_spawn_chance_permille", 0)
	s.VisitorMaxConcurrent = parseIntSetting(values, "visitor_max_concurrent", 2)
	s.VisitorMinStayMinutes = parseIntSetting(values, "visitor_min_stay_minutes", 240)
	s.VisitorMaxStayMinutes = parseIntSetting(values, "visitor_max_stay_minutes", 1440)
	s.VisitorTickInterval = parseDurationSetting(values, "visitor_tick_interval_seconds", 60*time.Second)
	// LLM-372: returner comeback window (wall-clock days).
	s.VisitorReturnMinDays = parseIntSetting(values, "visitor_return_min_days", sim.DefaultVisitorReturnMinDays)
	s.VisitorReturnMaxDays = parseIntSetting(values, "visitor_return_max_days", sim.DefaultVisitorReturnMaxDays)
	// LLM-410: wholesale factor pack + purse.
	s.VisitorFactorPackUnits = parseIntSetting(values, "visitor_factor_pack_units", sim.DefaultVisitorFactorPackUnits)
	s.VisitorFactorPurseMin = parseIntSetting(values, "visitor_factor_purse_min", sim.DefaultVisitorFactorPurseMin)
	s.VisitorFactorPurseMax = parseIntSetting(values, "visitor_factor_purse_max", sim.DefaultVisitorFactorPurseMax)
	// LLM-442: iron shipment size per factor visit.
	s.VisitorFactorIronUnits = parseIntSetting(values, "visitor_factor_iron_units", sim.DefaultVisitorFactorIronUnits)

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

	// Huddle silence conclusion (ZBBS-HOME-417).
	s.HuddleSilenceTimeout = parseDurationSetting(values, "huddle_silence_timeout_minutes", sim.HuddleSilenceTimeoutDefault)
	s.HuddleSilenceSweepCadence = parseDurationSetting(values, "huddle_silence_sweep_cadence_seconds", sim.HuddleSilenceSweepCadenceDefault)

	// Huddle loop conclusion (LLM-159). huddle_loop_timeout_seconds is the master
	// enable: 0/unset leaves the loop sweep OFF. huddle_loop_max_turns is the
	// LLM-333 endurance arm's no-progress turn budget.
	// huddle_conversation_wind_down_seconds is the LLM-397 lingering arm's clock —
	// how long a conversation may run before it is steered toward a close; the hard
	// conclude lands one huddle_loop_timeout_seconds after that.
	s.HuddleLoopTimeout = parseDurationSetting(values, "huddle_loop_timeout_seconds", 0)
	s.HuddleLoopRepeatPercent = parseIntSetting(values, "huddle_loop_repeat_percent", sim.HuddleLoopRepeatPercentDefault)
	s.HuddleLoopSweepCadence = parseDurationSetting(values, "huddle_loop_sweep_cadence_seconds", sim.HuddleLoopSweepCadenceDefault)
	s.HuddleLoopMaxTurns = parseIntSetting(values, "huddle_loop_max_turns", sim.HuddleLoopMaxTurnsDefault)
	s.HuddleConversationWindDown = parseDurationSetting(values, "huddle_conversation_wind_down_seconds", sim.HuddleConversationWindDownDefault)

	// Seek-work coin ceiling (LLM-194). 0/unset falls back to the default at read time
	// via effectiveSeekWorkCoinCeiling, but seed the default here too so GET /settings
	// reports the live value and the checkpoint round-trips a concrete number.
	s.SeekWorkCoinCeiling = parseIntSetting(values, "seek_work_coin_ceiling", sim.SeekWorkCoinCeilingDefault)

	// Seek-work→eat redirect margin (LLM-276). Seeded like the ceiling so GET /settings
	// reports the live value and the checkpoint round-trips a concrete number; 0/unset
	// falls back to the default at read time via effectiveSeekWorkNeedYieldMargin.
	s.SeekWorkNeedYieldMargin = parseIntSetting(values, "seek_work_need_yield_margin", sim.SeekWorkNeedYieldMarginDefault)

	// Labor produce boost (LLM-224). Unset seeds the default; an explicit 0 sticks
	// and disables the boost (the off-switch, like farm_upkeep_coins_per_shovel).
	s.LaborProduceBoostPct = parseIntSetting(values, "labor_produce_boost_pct", sim.DefaultLaborProduceBoostPct)

	// Merchant working-capital floor (LLM-294). Unset seeds the default; an explicit 0
	// sticks and disables the conserve gate (the off-switch, like labor_produce_boost_pct).
	s.MerchantCoinFloor = parseIntSetting(values, "merchant_coin_floor", sim.MerchantCoinFloorDefault)

	// Eco mode (LLM-313). ON by default — unset seeds enabled with the default gaps;
	// an explicit false/0 sticks (eco_enabled false kills the whole feature; a zero
	// gap disables that bucket's throttle individually). Eco paces an unwatched
	// village; it no longer ends its conversations — the LLM-334 arc key
	// (eco_conversation_max_seconds) is retired and ignored, superseded by
	// huddle_conversation_wind_down_seconds above (LLM-397). An existing row for the
	// old key is inert; nothing reads it.
	s.EcoEnabled = parseBoolSetting(values, "eco_enabled", true)
	s.EcoSocialGap = parseDurationSetting(values, "eco_social_gap_seconds", sim.DefaultEcoSocialGap)
	s.EcoEconomyGap = parseDurationSetting(values, "eco_economy_gap_seconds", sim.DefaultEcoEconomyGap)

	// Cross-huddle conversation continuity (LLM-170). ON by default — the ring
	// carry-over is pure perception legibility; the loop-state carry is inert
	// unless the loop sweep above is enabled.
	s.HuddleContinuityWindow = parseDurationSetting(values, "huddle_continuity_window_seconds", sim.HuddleContinuityWindowDefault)

	// PC presence staleness (ZBBS-WORK-326).
	s.PCPresenceStaleAfter = parseDurationSetting(values, "pc_presence_stale_seconds", sim.DefaultPCPresenceStaleAfter)

	// Conversation turn-state liveness windows (ZBBS-WORK-370).
	s.PCAwaitReplyWindow = parseDurationSetting(values, "pc_await_reply_window_seconds", sim.DefaultPCAwaitReplyWindow)
	s.NPCAwaitReplyWindow = parseDurationSetting(values, "npc_await_reply_window_seconds", sim.DefaultNPCAwaitReplyWindow)

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

// clampNonNegSetting is parseIntSetting for a value that must not be negative
// (LLM-439). A stored negative is clamped to 0 — keeping the always-live village
// booting on a fat-fingered config instead of failing at boot or, worse, letting
// the bad value through (a negative cold recovery rate flips recovery into
// accrual). The clamp is recorded as a sim.SettingWarning appended to *warnings,
// which the umbilical /settings surface reports, and logged. A missing/malformed
// row falls through parseIntSetting to def (in range by construction) and is not
// flagged. Regenerated at every boot from the stored value, so the warning
// survives restart for as long as the misconfiguration does.
func clampNonNegSetting(values map[string]string, key string, def int, warnings *[]sim.SettingWarning) int {
	v := parseIntSetting(values, key, def)
	if v < 0 {
		log.Printf("pg environment: setting %q=%d is out of range (must be >= 0) — clamping to 0", key, v)
		*warnings = append(*warnings, sim.SettingWarning{
			Key:     key,
			Raw:     v,
			Clamped: 0,
			Reason:  "value must be 0 or greater; clamped to 0",
		})
		return 0
	}
	return v
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

// SaveMutableSettings upserts the runtime-tunable settings the admin config
// write routes own (ZBBS-WORK-363) into the setting kv table, inside the
// checkpoint Tx. ONLY these keys are written — the rest of the setting
// table is load-once, operator-tuned out of band, so a full settings replace
// would clobber a direct DB edit with the startup-loaded value. Values are
// stored as strings (the load path parses them via parseFloatSetting /
// parseBoolSetting / parseIntSetting), so they're formatted to match: floats
// with the minimal round-trippable form, bool as "true"/"false", ints decimal.
func (r *EnvironmentRepo) SaveMutableSettings(ctx context.Context, tx sim.Tx, ms sim.MutableWorldSettings) error {
	rows := [...]struct {
		key string
		val string
	}{
		{"world_zoom_min_admin", strconv.FormatFloat(ms.ZoomMinAdmin, 'f', -1, 64)},
		{"world_zoom_min_regular", strconv.FormatFloat(ms.ZoomMinRegular, 'f', -1, 64)},
		{"agent_ticks_paused", strconv.FormatBool(ms.AgentTicksPaused)},
		// Stall wear knobs (LLM-118) — live-tuned via the umbilical, persisted here.
		{"stall_wear_per_coin", strconv.Itoa(ms.StallWearPerCoin)},
		{"stall_wear_repair_threshold", strconv.Itoa(ms.StallWearRepairThreshold)},
		{"stall_wear_degrade_threshold", strconv.Itoa(ms.StallWearDegradeThreshold)},
		{"stall_nails_per_repair", strconv.Itoa(ms.StallNailsPerRepair)},
		{"stall_repair_duration_seconds", strconv.Itoa(ms.StallRepairDurationSeconds)},
		{"stall_degraded_produce_pct", strconv.Itoa(ms.StallDegradedProducePct)},
		// Farm upkeep wealth-tax knobs (LLM-215) — live-tuned via the umbilical, persisted here.
		{"farm_upkeep_floor", strconv.Itoa(ms.FarmUpkeepFloor)},
		{"farm_upkeep_coins_per_shovel", strconv.Itoa(ms.FarmUpkeepCoinsPerShovel)},
		// Huddle loop-sweep knobs (LLM-159; enabled/tuned via the umbilical in
		// LLM-183) — live-tuned, persisted here. Stored in seconds; the load path
		// parses huddle_loop_timeout_seconds / huddle_loop_sweep_cadence_seconds via
		// parseDurationSetting and huddle_loop_repeat_percent via parseIntSetting.
		{"huddle_loop_timeout_seconds", strconv.Itoa(ms.HuddleLoopTimeoutSeconds)},
		{"huddle_loop_repeat_percent", strconv.Itoa(ms.HuddleLoopRepeatPercent)},
		{"huddle_loop_sweep_cadence_seconds", strconv.Itoa(ms.HuddleLoopSweepCadenceSeconds)},
		{"huddle_loop_max_turns", strconv.Itoa(ms.HuddleLoopMaxTurns)},
		{"huddle_conversation_wind_down_seconds", strconv.Itoa(ms.HuddleConversationWindDownSeconds)},
		// Seek-work coin ceiling (LLM-194) — live-tuned via the umbilical, persisted
		// here; the load path parses seek_work_coin_ceiling via parseIntSetting.
		{"seek_work_coin_ceiling", strconv.Itoa(ms.SeekWorkCoinCeiling)},
		// Seek-work→eat redirect margin (LLM-276) — live-tuned via the umbilical,
		// persisted here; the load path parses seek_work_need_yield_margin via parseIntSetting.
		{"seek_work_need_yield_margin", strconv.Itoa(ms.SeekWorkNeedYieldMargin)},
		// Labor produce boost (LLM-224) — live-tuned via the umbilical, persisted
		// here; the load path parses labor_produce_boost_pct via parseIntSetting.
		{"labor_produce_boost_pct", strconv.Itoa(ms.LaborProduceBoostPct)},
		// Merchant working-capital floor (LLM-294) — live-tuned via the umbilical,
		// persisted here; the load path parses merchant_coin_floor via parseIntSetting.
		{"merchant_coin_floor", strconv.Itoa(ms.MerchantCoinFloor)},
		// Eco mode knobs (LLM-313) — live-tuned via the umbilical, persisted here.
		// Gaps stored in seconds; the load path parses eco_enabled via
		// parseBoolSetting and the gaps via parseDurationSetting.
		{"eco_enabled", strconv.FormatBool(ms.EcoEnabled)},
		{"eco_social_gap_seconds", strconv.Itoa(ms.EcoSocialGapSeconds)},
		{"eco_economy_gap_seconds", strconv.Itoa(ms.EcoEconomyGapSeconds)},
	}
	for _, row := range rows {
		if _, err := tx.Exec(ctx, upsertSettingSQL, row.key, row.val); err != nil {
			return fmt.Errorf("pg environment SaveMutableSettings: upsert %s: %w", row.key, err)
		}
	}
	return nil
}
