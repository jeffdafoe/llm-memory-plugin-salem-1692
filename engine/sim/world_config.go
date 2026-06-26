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
// provided, or a provided value is negative (→ 400 at the umbilical route).
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
// operator can nudge one or several; at least one must be present, and a provided
// value must be >= 0 (StallWearPerCoin==0 disables wear; a 0 threshold disables
// that transition). Durability rides the periodic checkpoint (MutableWorldSettings
// → SaveMutableSettings), so a live change survives restart.
func SetStallWearSettings(perCoin, repairThreshold, degradeThreshold, nailsPerRepair, durationSeconds *int) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			knobs := []*int{perCoin, repairThreshold, degradeThreshold, nailsPerRepair, durationSeconds}
			hasOne := false
			for _, p := range knobs {
				if p == nil {
					continue
				}
				if *p < 0 {
					return nil, ErrInvalidStallWearSetting
				}
				hasOne = true
			}
			if !hasOne {
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
