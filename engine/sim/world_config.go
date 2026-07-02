package sim

import (
	"errors"
	"math"
	"time"
)

// world_config.go — admin world-config mutations (ZBBS-WORK-363): the write
// side of the config panel. Each command mutates the runtime-tunable subset of
// WorldSettings in-memory on the world goroutine and emits a WS event for live
// client updates. Durability rides the periodic checkpoint
// (BuildCheckpointSnapshot → MutableWorldSettings → pg.SaveWorld), the same
// model object placement uses — these are NOT written through to pg immediately.

// ErrInvalidZoomSetting is returned by SetZoomSettings when neither floor is
// provided, or a provided value is non-finite / non-positive (→ 400 at HTTP).
var ErrInvalidZoomSetting = errors.New("invalid zoom setting")

// SetZoomSettingsResult echoes the post-change zoom floors.
type SetZoomSettingsResult struct {
	ZoomMinAdmin   float64
	ZoomMinRegular float64
}

// SetZoomSettings returns a Command that updates the camera zoom floors. admin
// and regular are independently optional (nil = leave that floor unchanged) so
// the panel can save one or both; at least one must be present. A provided
// value must be finite and > 0. Emits ZoomSettingsChanged carrying the
// post-change floors so connected clients reload live.
func SetZoomSettings(admin, regular *float64) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			if admin == nil && regular == nil {
				return nil, ErrInvalidZoomSetting
			}
			if admin != nil && !validZoomFloor(*admin) {
				return nil, ErrInvalidZoomSetting
			}
			if regular != nil && !validZoomFloor(*regular) {
				return nil, ErrInvalidZoomSetting
			}
			if admin != nil {
				w.Settings.ZoomMinAdmin = *admin
			}
			if regular != nil {
				w.Settings.ZoomMinRegular = *regular
			}
			w.emit(&ZoomSettingsChanged{
				ZoomMinAdmin:   w.Settings.ZoomMinAdmin,
				ZoomMinRegular: w.Settings.ZoomMinRegular,
				At:             time.Now().UTC(),
			})
			return SetZoomSettingsResult{
				ZoomMinAdmin:   w.Settings.ZoomMinAdmin,
				ZoomMinRegular: w.Settings.ZoomMinRegular,
			}, nil
		},
	}
}

func validZoomFloor(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0) && v > 0
}

// ErrInvalidStallWearSetting is returned by SetStallWearSettings when no knob is
// provided, a value is out of range (a negative perCoin/threshold, a non-positive
// nails/duration), or the resulting thresholds contradict (degrade below an
// enabled repair) — all → 400 at the umbilical route.
var ErrInvalidStallWearSetting = errors.New("invalid stall wear setting")

// StallWearSettingsResult echoes the post-change stall wear knobs.
type StallWearSettingsResult struct {
	StallWearPerCoin           int
	StallWearRepairThreshold   int
	StallWearDegradeThreshold  int
	StallNailsPerRepair        int
	StallRepairDurationSeconds int
}

// SetStallWearSettings returns a Command that live-tunes the LLM-118 stall wear
// knobs. Each is independently optional (nil = leave that knob unchanged) so the
// operator can nudge one or several; at least one must be present. Range rules:
// perCoin and the two thresholds must be >= 0 (StallWearPerCoin==0 disables wear,
// a 0 threshold disables that transition); nails-per-repair and duration must be
// > 0; and an enabled degrade threshold must be >= an enabled repair threshold
// (checked against the resulting live values) so a stall can't degrade before it
// can be repaired. Durability rides the periodic checkpoint (MutableWorldSettings
// → SaveMutableSettings), so a live change survives restart.
func SetStallWearSettings(perCoin, repairThreshold, degradeThreshold, nailsPerRepair, durationSeconds *int) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			// Per-knob validation. perCoin and the two thresholds may be 0 (0
			// disables wear / that transition); nails and duration must be positive
			// (a free or instant repair is not a meaningful mode). At least one knob
			// must be present.
			// MaxInt32 upper bound keeps any knob × a sale amount (also bounded by
			// MaxPayWithItemAmount) well inside int64, so wear accrual can't overflow.
			hasOne := false
			for _, p := range []*int{perCoin, repairThreshold, degradeThreshold} {
				if p != nil {
					hasOne = true
					if *p < 0 || *p > math.MaxInt32 {
						return nil, ErrInvalidStallWearSetting
					}
				}
			}
			for _, p := range []*int{nailsPerRepair, durationSeconds} {
				if p != nil {
					hasOne = true
					if *p <= 0 || *p > math.MaxInt32 {
						return nil, ErrInvalidStallWearSetting
					}
				}
			}
			if !hasOne {
				return nil, ErrInvalidStallWearSetting
			}
			// Threshold relationship, checked against the RESULTING live values (a
			// partial update is validated against the other threshold's current
			// value): an enabled degrade threshold must sit at or above an enabled
			// repair threshold, else a stall could degrade — and block its own
			// sales — before it ever reaches the wear level that lets it be repaired.
			// (StallRepairable de-bricks at runtime regardless; this stops the
			// operator setting a self-contradicting config in the first place.)
			resulting := func(p *int, cur int) int {
				if p != nil {
					return *p
				}
				return cur
			}
			newRepair := resulting(repairThreshold, w.Settings.StallWearRepairThreshold)
			newDegrade := resulting(degradeThreshold, w.Settings.StallWearDegradeThreshold)
			if newDegrade > 0 && (newRepair == 0 || newRepair > newDegrade) {
				return nil, ErrInvalidStallWearSetting
			}
			if perCoin != nil {
				w.Settings.StallWearPerCoin = *perCoin
			}
			if repairThreshold != nil {
				w.Settings.StallWearRepairThreshold = *repairThreshold
			}
			if degradeThreshold != nil {
				w.Settings.StallWearDegradeThreshold = *degradeThreshold
			}
			if nailsPerRepair != nil {
				w.Settings.StallNailsPerRepair = *nailsPerRepair
			}
			if durationSeconds != nil {
				w.Settings.StallRepairDurationSeconds = *durationSeconds
			}
			return StallWearSettingsResult{
				StallWearPerCoin:           w.Settings.StallWearPerCoin,
				StallWearRepairThreshold:   w.Settings.StallWearRepairThreshold,
				StallWearDegradeThreshold:  w.Settings.StallWearDegradeThreshold,
				StallNailsPerRepair:        w.Settings.StallNailsPerRepair,
				StallRepairDurationSeconds: w.Settings.StallRepairDurationSeconds,
			}, nil
		},
	}
}

// SetAgentTicksPausedResult echoes the post-change pause state.
type SetAgentTicksPausedResult struct {
	Paused bool
}

// SetAgentTicksPaused returns a Command that toggles the global LLM-agent
// activity pause (WorldSettings.AgentTicksPaused — suppresses reactive NPC
// ticks + chronicler fires while worker schedulers keep running). Emits
// AgentTicksPausedChanged so the config panel's checkbox reflects the new state
// across connected admins.
func SetAgentTicksPaused(paused bool) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			w.Settings.AgentTicksPaused = paused
			w.emit(&AgentTicksPausedChanged{
				Paused: paused,
				At:     time.Now().UTC(),
			})
			return SetAgentTicksPausedResult{Paused: paused}, nil
		},
	}
}

// ErrInvalidHuddleLoopSetting is returned by SetHuddleLoopSettings when no knob is
// provided, or a value is out of range (a negative timeout, a repeat_percent outside
// 1-100, a non-positive cadence) — all → 400 at the umbilical route.
var ErrInvalidHuddleLoopSetting = errors.New("invalid huddle loop setting")

// HuddleLoopSettingsResult echoes the post-change huddle loop-sweep knobs in wire
// units (seconds + percent).
type HuddleLoopSettingsResult struct {
	TimeoutSeconds int
	RepeatPercent  int
	CadenceSeconds int
}

// SetHuddleLoopSettings returns a Command that live-tunes the huddle
// conversational-loop sweep knobs (LLM-159). Each is independently optional (nil =
// leave that knob unchanged) so the operator can flip the master enable or nudge a
// single threshold; at least one must be present. Range rules: timeoutSeconds >= 0
// (0 disables the whole sweep AND the per-tick ConversationLooping steer — the
// master off-switch); repeatPercent in [1,100]; cadenceSeconds > 0. The MaxInt32
// upper bound on the two second-valued knobs keeps seconds*time.Second well inside
// int64.
//
// Takes effect immediately: huddleLoopEnabled (HuddleLoopTimeout > 0) is re-read
// every sweep scan and every republish, so flipping timeout on arms the sweep + the
// steer within one scan cadence. A cadence change applies on the sweep timer's NEXT
// rearm (the AfterFunc reads HuddleLoopSweepCadence then), not to the in-flight
// timer. Durability rides the periodic checkpoint (MutableWorldSettings →
// SaveMutableSettings upserts huddle_loop_*_seconds / huddle_loop_repeat_percent
// into the setting table), so a live change survives restart.
func SetHuddleLoopSettings(timeoutSeconds, repeatPercent, cadenceSeconds *int) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			hasOne := false
			if timeoutSeconds != nil {
				hasOne = true
				if *timeoutSeconds < 0 || *timeoutSeconds > math.MaxInt32 {
					return nil, ErrInvalidHuddleLoopSetting
				}
			}
			if repeatPercent != nil {
				hasOne = true
				if *repeatPercent < 1 || *repeatPercent > 100 {
					return nil, ErrInvalidHuddleLoopSetting
				}
			}
			if cadenceSeconds != nil {
				hasOne = true
				if *cadenceSeconds <= 0 || *cadenceSeconds > math.MaxInt32 {
					return nil, ErrInvalidHuddleLoopSetting
				}
			}
			if !hasOne {
				return nil, ErrInvalidHuddleLoopSetting
			}
			if timeoutSeconds != nil {
				w.Settings.HuddleLoopTimeout = time.Duration(*timeoutSeconds) * time.Second
			}
			if repeatPercent != nil {
				w.Settings.HuddleLoopRepeatPercent = *repeatPercent
			}
			if cadenceSeconds != nil {
				w.Settings.HuddleLoopSweepCadence = time.Duration(*cadenceSeconds) * time.Second
			}
			return HuddleLoopSettingsResult{
				TimeoutSeconds: int(w.Settings.HuddleLoopTimeout / time.Second),
				RepeatPercent:  w.Settings.HuddleLoopRepeatPercent,
				CadenceSeconds: int(w.Settings.HuddleLoopSweepCadence / time.Second),
			}, nil
		},
	}
}

// ErrInvalidSeekWorkCeilingSetting is returned by SetSeekWorkCoinCeiling when the
// ceiling is missing or out of range (must be >= 1) — → 400 at the umbilical route.
var ErrInvalidSeekWorkCeilingSetting = errors.New("invalid seek-work ceiling setting")

// SeekWorkCeilingSettingResult echoes the post-change seek-work coin ceiling.
type SeekWorkCeilingSettingResult struct {
	CoinCeiling int
}

// SetSeekWorkCoinCeiling returns a Command that live-tunes the seek-work coin ceiling
// (LLM-194): the coin balance at/above which a workless worker stops seeking/soliciting
// work and drains its purse via ordinary consumption. Required (a single knob, nil =
// nothing to do) and must be >= 1 — a zero/negative ceiling is meaningless (it would
// suppress seek-work for every worker, since coins >= 0 is always true; the load/read
// paths treat 0 as "use the default" via effectiveSeekWorkCoinCeiling). To DISABLE the
// shelf and restore always-seek, set a very large ceiling. The MaxInt32 upper bound
// keeps the value comfortably inside int. Takes effect immediately — the warrant gate
// reads w.Settings live and the perception gate reads the next published snapshot
// (which copies the effective value) — AND persists on the next checkpoint via
// MutableWorldSettings, so a live change survives restart.
func SetSeekWorkCoinCeiling(ceiling *int) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			if ceiling == nil || *ceiling < 1 || *ceiling > math.MaxInt32 {
				return nil, ErrInvalidSeekWorkCeilingSetting
			}
			w.Settings.SeekWorkCoinCeiling = *ceiling
			return SeekWorkCeilingSettingResult{CoinCeiling: w.Settings.SeekWorkCoinCeiling}, nil
		},
	}
}

// ErrInvalidLaborProduceBoostSetting is returned by SetLaborProduceBoostPct when the
// percent is missing or out of range (must be >= 0) — → 400 at the umbilical route.
var ErrInvalidLaborProduceBoostSetting = errors.New("invalid labor produce boost setting")

// LaborProduceBoostSettingResult echoes the post-change per-worker boost percent.
type LaborProduceBoostSettingResult struct {
	BoostPct int
}

// SetLaborProduceBoostPct returns a Command that live-tunes the per-worker produce
// boost (LLM-224): each worker laboring at their employer's establishment adds this
// percent of the keeper's base rate to the produce tick. Required (a single knob,
// nil = nothing to do) and must be >= 0 — 0 is the explicit off-switch (labor buys
// no output, the pre-LLM-224 behavior). Takes effect immediately — the produce tick
// reads w.Settings live and the perception hire-value cue reads the next published
// snapshot — AND persists on the next checkpoint via MutableWorldSettings, so a live
// change survives restart.
func SetLaborProduceBoostPct(pct *int) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			if pct == nil || *pct < 0 || *pct > math.MaxInt32 {
				return nil, ErrInvalidLaborProduceBoostSetting
			}
			w.Settings.LaborProduceBoostPct = *pct
			return LaborProduceBoostSettingResult{BoostPct: w.Settings.LaborProduceBoostPct}, nil
		},
	}
}

// ErrInvalidFarmUpkeepSetting is returned by SetFarmUpkeepSettings when no knob is
// provided or a value is out of range (must be >= 0) — → 400 at the umbilical route.
var ErrInvalidFarmUpkeepSetting = errors.New("invalid farm upkeep setting")

// FarmUpkeepSettingsResult echoes the post-change farm-upkeep knobs.
type FarmUpkeepSettingsResult struct {
	FarmUpkeepFloor          int
	FarmUpkeepCoinsPerShovel int
}

// SetFarmUpkeepSettings returns a Command that live-tunes the LLM-215 farm wealth-tax
// knobs. Each is independently optional (nil = leave that knob unchanged) so the
// operator can nudge one or both; at least one must be present. Range: both must be
// >= 0 — the floor may be 0 (tax from the first coin), and FarmUpkeepCoinsPerShovel==0
// disables the feature entirely (the off-switch, mirroring StallWearPerCoin==0). The
// MaxInt32 upper bound keeps each value comfortably inside int. Takes effect
// immediately — the daily assessment reads w.Settings live and the perception cue reads
// the next published snapshot (which copies both) — AND persists on the next checkpoint
// via MutableWorldSettings, so a live change survives restart.
func SetFarmUpkeepSettings(floor, coinsPerShovel *int) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			hasOne := false
			for _, p := range []*int{floor, coinsPerShovel} {
				if p != nil {
					hasOne = true
					if *p < 0 || *p > math.MaxInt32 {
						return nil, ErrInvalidFarmUpkeepSetting
					}
				}
			}
			if !hasOne {
				return nil, ErrInvalidFarmUpkeepSetting
			}
			if floor != nil {
				w.Settings.FarmUpkeepFloor = *floor
			}
			if coinsPerShovel != nil {
				w.Settings.FarmUpkeepCoinsPerShovel = *coinsPerShovel
			}
			return FarmUpkeepSettingsResult{
				FarmUpkeepFloor:          w.Settings.FarmUpkeepFloor,
				FarmUpkeepCoinsPerShovel: w.Settings.FarmUpkeepCoinsPerShovel,
			}, nil
		},
	}
}
