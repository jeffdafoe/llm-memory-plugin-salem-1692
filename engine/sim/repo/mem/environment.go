package mem

import (
	"context"
	"fmt"
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
			// Constable rounds (LLM-514) — mirror the pg parse fallbacks so a
			// mem-backed world runs the constable's rounds like prod.
			ConstableRoundsInterval: sim.DefaultConstableRoundsInterval,
			// Cold exposure + hearth (LLM-412) — mirror the pg parse fallbacks so a
			// mem-backed world feels the weather like prod does.
			ColdStormOutdoorsPerMinuteX100:     sim.DefaultColdStormOutdoorsPerMinuteX100,
			ColdStormIndoorsPerMinuteX100:      sim.DefaultColdStormIndoorsPerMinuteX100,
			ColdWarmGarmentPerMinuteX100:       sim.DefaultColdWarmGarmentPerMinuteX100,
			ColdThreadbareGarmentPerMinuteX100: sim.DefaultColdThreadbareGarmentPerMinuteX100,
			ColdNightMultiplierX100:            sim.DefaultColdNightMultiplierX100,
			ColdWarmRecoveryPerMinuteX100:      sim.DefaultColdWarmRecoveryPerMinuteX100,
			ColdClearRecoveryPerMinuteX100:     sim.DefaultColdClearRecoveryPerMinuteX100,
			ColdProduceSapPct:                  sim.DefaultColdProduceSapPct,
			HearthBurnMinutesPerWood:           sim.DefaultHearthBurnMinutesPerWood,
			HearthMaxBankMinutes:               sim.DefaultHearthMaxBankMinutes,
			HearthLowMinutes:                   sim.DefaultHearthLowMinutes,
			StokeWoodPerStoke:                  sim.DefaultStokeWoodPerStoke,
			StokeDurationSeconds:               sim.DefaultStokeDurationSeconds,
			// Garment wear (LLM-422) — mirror the pg parse fallbacks.
			GarmentWearPerMinute:          sim.DefaultGarmentWearPerMinute,
			GarmentThreadbareFractionX100: sim.DefaultGarmentThreadbareFractionX100,
			// Wholesale factor pack + purse (LLM-410) — mirror the pg parse fallbacks so a
			// mem-backed factor spawns with a full bale and a heavy purse like prod.
			VisitorFactorPackUnits: sim.DefaultVisitorFactorPackUnits,
			VisitorFactorPurseMin:  sim.DefaultVisitorFactorPurseMin,
			VisitorFactorPurseMax:  sim.DefaultVisitorFactorPurseMax,
			// Iron shipment per factor visit (LLM-442) — same mirror.
			VisitorFactorIronUnits: sim.DefaultVisitorFactorIronUnits,
			// Salt shipment per factor visit (LLM-444) — same mirror.
			VisitorFactorSaltUnits: sim.DefaultVisitorFactorSaltUnits,
			// Thread shipment per factor visit (LLM-625) — same mirror.
			VisitorFactorThreadUnits: sim.DefaultVisitorFactorThreadUnits,
			// Grounded merchant errand direction/class weights (LLM-455) — mirror the pg
			// fallbacks so a mem-backed spawn picks buy/sell + merchant/passer like prod.
			// Coin band low/high default 0 (unconfigured) so no explicit mirror is needed.
			VisitorSellWeightPermille: sim.DefaultVisitorSellWeightPermille,
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

// SaveMutableSettings applies the checkpoint's persistable rows back onto the
// in-memory settings, so a mem-backed save -> load round-trip matches prod.
//
// This used to be a field-by-field mirror of the pg writer's row literal, and
// had drifted: the eco knobs, the merchant coin floor, huddle max-turns and the
// conversation wind-down were all persisted by pg and silently dropped here, so
// a mem-backed round-trip disagreed with production. Applying the registry rows
// removes the mirror entirely — whatever pg would write is what lands here.
//
// A malformed row is a programming error (the rows were formatted by the same
// registry that parses them), so it surfaces as an error rather than being
// skipped: a checkpoint that cannot round-trip its own encoding should fail
// loudly in tests, not persist half the settings.
func (r *EnvironmentRepo) SaveMutableSettings(_ context.Context, _ sim.Tx, ms sim.MutableWorldSettings) error {
	for key, raw := range ms.Rows {
		if _, err := sim.ApplySetting(&r.settings, key, raw); err != nil {
			return fmt.Errorf("mem environment SaveMutableSettings: %w", err)
		}
	}
	return nil
}
