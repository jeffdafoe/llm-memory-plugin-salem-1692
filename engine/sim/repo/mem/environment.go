package mem

import (
	"context"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// EnvironmentRepo is an in-memory implementation of sim.EnvironmentRepo.
//
// Holds Environment + Phase + Settings as the source of truth. Tests Seed
// the values that production would read from the world_phase / setting
// tables.
type EnvironmentRepo struct {
	env      sim.WorldEnvironment
	phase    sim.Phase
	settings sim.WorldSettings
	loaded   bool
}

// NewEnvironmentRepo returns an empty mem EnvironmentRepo. Without a Seed
// call, Load returns zero values plus fallback settings — the world
// boots in a degenerate but non-crashing state, matching how legacy
// loadWorldConfig handles a fresh deploy.
func NewEnvironmentRepo() *EnvironmentRepo {
	loc, _ := time.LoadLocation(sim.DefaultTimezone)
	return &EnvironmentRepo{
		settings: sim.WorldSettings{
			DawnTime:                   sim.DefaultDawn,
			DuskTime:                   sim.DefaultDusk,
			RotationTime:               sim.DefaultRotationTime,
			Timezone:                   sim.DefaultTimezone,
			Location:                   loc,
			ZoomMinAdmin:               sim.DefaultZoomMinAdmin,
			ZoomMinRegular:             sim.DefaultZoomMinRegular,
			NeedsTickAmount:            sim.DefaultNeedsTickAmount,
			NeedThresholds:             sim.DefaultNeedThresholds(),
			TirednessCriticalThreshold: (sim.NeedMax*sim.DefaultTirednessCriticalThresholdPct + 99) / 100,
			MovementFatiguePerTileX100: sim.DefaultMovementFatiguePerTileX100,
			RestockReorderPct:          sim.DefaultRestockReorderPct,
			LodgingCheckOutHour:        11,
			LodgingBedtimeHour:         sim.DefaultLodgingBedtimeHour,
			ShiftLatenessWindowMinutes: sim.DefaultShiftLatenessWindowMinutes,
			// Cold exposure + hearth (LLM-412) — mirror the pg parse fallbacks so a
			// mem-backed world feels the weather like prod does.
			ColdStormOutdoorsPerMinuteX100: sim.DefaultColdStormOutdoorsPerMinuteX100,
			ColdStormIndoorsPerMinuteX100:  sim.DefaultColdStormIndoorsPerMinuteX100,
			ColdNightMultiplierX100:        sim.DefaultColdNightMultiplierX100,
			ColdWarmRecoveryPerMinuteX100:  sim.DefaultColdWarmRecoveryPerMinuteX100,
			ColdClearRecoveryPerMinuteX100: sim.DefaultColdClearRecoveryPerMinuteX100,
			ColdProduceSapPct:              sim.DefaultColdProduceSapPct,
			HearthBurnMinutesPerWood:       sim.DefaultHearthBurnMinutesPerWood,
			HearthMaxBankMinutes:           sim.DefaultHearthMaxBankMinutes,
			HearthLowMinutes:               sim.DefaultHearthLowMinutes,
			StokeWoodPerStoke:              sim.DefaultStokeWoodPerStoke,
			StokeDurationSeconds:           sim.DefaultStokeDurationSeconds,
		},
		phase: sim.PhaseDay,
	}
}

// Seed sets the loaded environment + phase + settings. Tests call this
// before LoadWorld to control startup state.
func (r *EnvironmentRepo) Seed(env sim.WorldEnvironment, phase sim.Phase, settings sim.WorldSettings) {
	r.env = env
	r.phase = phase
	r.settings = settings
	r.loaded = true
}

func (r *EnvironmentRepo) Load(_ context.Context) (sim.WorldEnvironment, sim.Phase, sim.WorldSettings, error) {
	return r.env, r.phase, r.settings, nil
}

func (r *EnvironmentRepo) SaveSnapshot(_ context.Context, _ sim.Tx, env sim.WorldEnvironment, phase sim.Phase) error {
	r.env = env
	r.phase = phase
	return nil
}

func (r *EnvironmentRepo) SaveMutableSettings(_ context.Context, _ sim.Tx, ms sim.MutableWorldSettings) error {
	r.settings.ZoomMinAdmin = ms.ZoomMinAdmin
	r.settings.ZoomMinRegular = ms.ZoomMinRegular
	r.settings.AgentTicksPaused = ms.AgentTicksPaused
	// Mirror the pg writeback so a mem-backed save→load round-trip matches prod:
	// the FULL mutable subset, not just zoom/pause. Stall wear (LLM-118) was
	// missing here; huddle-loop (LLM-183) added alongside. The *_seconds fields are
	// held as Durations in WorldSettings, so convert back from the snapshot's ints.
	r.settings.StallWearPerCoin = ms.StallWearPerCoin
	r.settings.StallWearRepairThreshold = ms.StallWearRepairThreshold
	r.settings.StallWearDegradeThreshold = ms.StallWearDegradeThreshold
	r.settings.StallNailsPerRepair = ms.StallNailsPerRepair
	r.settings.StallRepairDurationSeconds = ms.StallRepairDurationSeconds
	r.settings.FarmUpkeepFloor = ms.FarmUpkeepFloor
	r.settings.FarmUpkeepCoinsPerShovel = ms.FarmUpkeepCoinsPerShovel
	r.settings.HuddleLoopTimeout = time.Duration(ms.HuddleLoopTimeoutSeconds) * time.Second
	r.settings.HuddleLoopRepeatPercent = ms.HuddleLoopRepeatPercent
	r.settings.HuddleLoopSweepCadence = time.Duration(ms.HuddleLoopSweepCadenceSeconds) * time.Second
	r.settings.SeekWorkCoinCeiling = ms.SeekWorkCoinCeiling
	r.settings.SeekWorkNeedYieldMargin = ms.SeekWorkNeedYieldMargin
	r.settings.LaborProduceBoostPct = ms.LaborProduceBoostPct
	return nil
}
