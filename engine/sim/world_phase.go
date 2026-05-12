package sim

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"
)

// World day/night cycle — in-memory port of legacy engine/world_phase.go.
//
// State lives on World.Phase + World.Environment.LastTransitionAt. A
// background goroutine (RunPhaseTicker) wakes once a minute, figures out
// which scheduled boundary is most recent, and — if not already processed —
// submits an ApplyPhaseTransition command to the world goroutine.
//
// PER-OBJECT FLIPS (lamps/torches/campfires changing current_state at the
// boundary) are STUBBED in this port: village_object isn't in the sim
// package yet, so the transition records the phase change and (eventually)
// fires a WS broadcast but does NOT bulk-update object state. When
// village_object/asset_state are ported in a later phase, the flip path
// gets wired in — likely as a separate command issued by ApplyPhaseTransition
// once it owns the phase mutation.
//
// HTTP handlers from legacy world_phase.go (handleGetWorldState, handleForcePhase,
// handleSetAgentTicksPaused, handleSetZoomSettings) are deferred to the HTTP-
// layer port. Internally, ApplyPhaseTransition is invokable as a command;
// admin endpoints will translate JSON to that command at cutover.

// PhaseDefaults are the fallback values when settings aren't loaded yet.
const (
	DefaultTimezone     = "America/New_York"
	DefaultDawn         = "07:00"
	DefaultDusk         = "19:00"
	DefaultRotationTime = "00:00"

	DefaultZoomMinAdmin   = 0.1
	DefaultZoomMinRegular = 0.3
)

// PhaseTickerInterval is how often RunPhaseTicker wakes to check boundaries.
// One minute matches legacy cadence — fast enough to land transitions
// within the minute of their scheduled time without busy-spinning.
const PhaseTickerInterval = time.Minute

// ParseHM splits "HH:MM" into hour and minute integers.
func ParseHM(s string) (hour, minute int, err error) {
	parts := strings.Split(s, ":")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("expected HH:MM, got %q", s)
	}
	hour, err = strconv.Atoi(parts[0])
	if err != nil || hour < 0 || hour > 23 {
		return 0, 0, fmt.Errorf("invalid hour in %q", s)
	}
	minute, err = strconv.Atoi(parts[1])
	if err != nil || minute < 0 || minute > 59 {
		return 0, 0, fmt.Errorf("invalid minute in %q", s)
	}
	return hour, minute, nil
}

// MostRecentBoundary returns the phase and wall-clock time of the most
// recent dawn/dusk boundary at or before now. The search window is 24h —
// always contains at least two boundaries.
func MostRecentBoundary(now time.Time, dawnH, dawnM, duskH, duskM int) (phase Phase, at time.Time) {
	loc := now.Location()
	y, mo, d := now.Date()
	todayDawn := time.Date(y, mo, d, dawnH, dawnM, 0, 0, loc)
	todayDusk := time.Date(y, mo, d, duskH, duskM, 0, 0, loc)

	candidates := []struct {
		t     time.Time
		phase Phase
	}{
		{todayDawn.Add(-24 * time.Hour), PhaseDay},
		{todayDusk.Add(-24 * time.Hour), PhaseNight},
		{todayDawn, PhaseDay},
		{todayDusk, PhaseNight},
	}

	var latestT time.Time
	var latestPhase Phase
	for _, c := range candidates {
		if !c.t.After(now) && c.t.After(latestT) {
			latestT = c.t
			latestPhase = c.phase
		}
	}
	return latestPhase, latestT
}

// NextBoundary returns the next dawn/dusk after now, plus the phase that
// boundary sets. Used by future GET /world endpoints for UI countdowns.
func NextBoundary(now time.Time, dawnH, dawnM, duskH, duskM int) (phase Phase, at time.Time) {
	loc := now.Location()
	y, mo, d := now.Date()
	todayDawn := time.Date(y, mo, d, dawnH, dawnM, 0, 0, loc)
	todayDusk := time.Date(y, mo, d, duskH, duskM, 0, 0, loc)

	candidates := []struct {
		t     time.Time
		phase Phase
	}{
		{todayDawn, PhaseDay},
		{todayDusk, PhaseNight},
		{todayDawn.Add(24 * time.Hour), PhaseDay},
		{todayDusk.Add(24 * time.Hour), PhaseNight},
	}

	for _, c := range candidates {
		if c.t.After(now) {
			return c.phase, c.t
		}
	}
	return PhaseDay, now // unreachable given 48h window
}

// PhaseTransitionResult is what ApplyPhaseTransition returns through the
// command reply. Mainly so admin force-phase responses can echo affected
// counts once the flip path is wired in.
type PhaseTransitionResult struct {
	From            Phase
	To              Phase
	At              time.Time
	ObjectsAffected int // 0 in v1 — village_object flips ported later
}

// ApplyPhaseTransition returns a Command that moves the world to newPhase
// and stamps LastTransitionAt. Safe to call when the current phase already
// matches — the result indicates no-op via From == To.
//
// In v1 this does NOT bulk-flip village_object state (those tables aren't
// in sim yet) and does NOT broadcast (Hub not ported). When village_object
// + Hub land, this handler grows the flip-and-broadcast tail.
func ApplyPhaseTransition(newPhase Phase) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			if newPhase != PhaseDay && newPhase != PhaseNight {
				return nil, fmt.Errorf("invalid phase %q", newPhase)
			}
			now := time.Now().UTC()
			from := w.Phase

			w.Phase = newPhase
			w.Environment.LastTransitionAt = now

			// TODO(rewrite): when village_object + asset_state land in sim,
			// determine + schedule per-object flips here (carrying a Gen
			// counter for supersede-detection on rapid force-day↔force-night).
			//
			// TODO(rewrite): when Hub/WS layer is ported, broadcast
			// world_phase_changed here so clients start the canvas tween at
			// the boundary.
			//
			// TODO(rewrite): when occupancy is ported, call
			// refreshNightOnlyOccupancyStates equivalent.

			log.Printf("sim/world_phase: transitioned %s -> %s at %s",
				from, newPhase, now.Format(time.RFC3339))

			return PhaseTransitionResult{
				From: from,
				To:   newPhase,
				At:   now,
			}, nil
		},
	}
}

// RunPhaseTicker owns the phase-boundary ticker goroutine. Wakes every
// PhaseTickerInterval, computes the most recent boundary, and submits an
// ApplyPhaseTransition command if the boundary hasn't been processed.
//
// Caller starts this in a goroutine alongside World.Run. Returns when ctx
// is cancelled.
//
// Reads dawn/dusk/timezone from World.Settings via a snapshot command (so
// it stays single-threaded with respect to the world goroutine). v1 reads
// once per tick — config changes mid-day take effect on the next tick.
func RunPhaseTicker(ctx context.Context, w *World) {
	t := time.NewTicker(PhaseTickerInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			checkAndTransition(ctx, w)
		}
	}
}

// checkAndTransition does one iteration of the ticker loop. Exported as a
// free function for testability — tests can call it directly with a
// pre-built world without waiting on a real timer.
func checkAndTransition(_ context.Context, w *World) {
	// Snapshot config via the world goroutine so we read consistent
	// dawn/dusk/location values relative to any concurrent settings
	// changes.
	cfgValue, err := w.Send(Command{
		Fn: func(world *World) (any, error) {
			return phaseTickerConfig{
				DawnTime: world.Settings.DawnTime,
				DuskTime: world.Settings.DuskTime,
				Location: world.Settings.Location,
				Last:     world.Environment.LastTransitionAt,
			}, nil
		},
	})
	if err != nil {
		log.Printf("sim/world_phase: snapshot config failed: %v", err)
		return
	}
	cfg := cfgValue.(phaseTickerConfig)

	if cfg.Location == nil {
		// Settings haven't been loaded yet — skip until next tick.
		return
	}

	dawnH, dawnM, err := ParseHM(cfg.DawnTime)
	if err != nil {
		log.Printf("sim/world_phase: bad dawn time %q: %v", cfg.DawnTime, err)
		return
	}
	duskH, duskM, err := ParseHM(cfg.DuskTime)
	if err != nil {
		log.Printf("sim/world_phase: bad dusk time %q: %v", cfg.DuskTime, err)
		return
	}

	now := time.Now().In(cfg.Location)
	targetPhase, boundaryAt := MostRecentBoundary(now, dawnH, dawnM, duskH, duskM)

	// Already processed this boundary (or one after it) — nothing to do.
	if !cfg.Last.Before(boundaryAt) {
		return
	}

	if _, err := w.Send(ApplyPhaseTransition(targetPhase)); err != nil {
		log.Printf("sim/world_phase: transition to %s failed: %v", targetPhase, err)
	}
}

type phaseTickerConfig struct {
	DawnTime string
	DuskTime string
	Location *time.Location
	Last     time.Time
}
