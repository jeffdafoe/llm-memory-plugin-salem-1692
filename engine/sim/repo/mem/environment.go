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
			ShiftLatenessWindowMinutes: sim.DefaultShiftLatenessWindowMinutes,
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
