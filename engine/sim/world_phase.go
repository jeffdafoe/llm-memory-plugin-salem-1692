package sim

import (
	"context"
	"fmt"
	"log"
	mathrand "math/rand/v2"
	"strconv"
	"strings"
	"time"
)

// Asset-state tags used by the phase-transition flip resolver.
const (
	TagDayActive         = "day-active"
	TagNightActive       = "night-active"
	TagLamplighterTarget = "lamplighter-target"
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
// counts.
type PhaseTransitionResult struct {
	From            Phase
	To              Phase
	At              time.Time
	Gen             uint64 // WorldEventGen at the time of transition
	ObjectsAffected int    // count of pending flips scheduled
}

// PendingFlip is one per-object state change scheduled by a phase
// transition. Carries the WorldEventGen captured at schedule time so a
// rapid force-day → force-night sequence cleanly invalidates the older
// transition's pending flips.
type PendingFlip struct {
	ObjectID      VillageObjectID
	NewState      string
	SpreadSeconds int    // 0 = fire immediately
	Gen           uint64 // WorldEventGen at the time of scheduling
}

// ApplyPhaseTransition returns a Command that moves the world to newPhase,
// stamps LastTransitionAt, bumps WorldEventGen, and schedules per-object
// state flips for every village_object whose asset has a state tagged with
// the target phase's tag. Idempotent on already-applied flips (the
// SetVillageObjectState command skips when current_state already matches).
//
// Flips fire asynchronously via time.AfterFunc with a uniform random delay
// in [0, asset.TransitionSpreadSeconds) seconds — the visual stagger lamps
// got on the legacy engine. Each fire is generation-checked against
// WorldEventGen so a subsequent transition supersedes its predecessor's
// pending flips cleanly.
//
// LAMPLIGHTER carve-out: lamplighter-target objects are excluded from
// the bulk flip when (and only when) an actor carries AttrLamplighter
// — the lamplighter cascade slice (engine/sim/cascade/npc_route.go)
// consumes the PhaseApplied event this command emits and walks the
// lamplighter NPC through them. With no lamplighter actor configured,
// the carve-out is skipped and lamplighter-target objects flip in the
// bulk pass — keeps deployment ordering forgiving (lamps don't get
// stuck if the cascade isn't registered yet or no actor has been
// tagged).
//
// HUB/WS broadcast (world_phase_changed and object_state_changed) is also
// stubbed pending the Hub layer port.
//
// OCCUPANCY REFRESH for night-only structures (legacy
// refreshNightOnlyOccupancyStates) is stubbed pending the occupancy port.
func ApplyPhaseTransition(newPhase Phase) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			if newPhase != PhaseDay && newPhase != PhaseNight {
				return nil, fmt.Errorf("invalid phase %q", newPhase)
			}
			now := time.Now().UTC()
			from := w.Phase

			w.Phase = newPhase
			// LastTransitionAt is the WALL-CLOCK instant the transition
			// command applied — not the scheduled boundary time. Two
			// implications:
			//   - The dedupe in checkAndTransition uses Last.Before(boundary)
			//     so a ticker fire at 19:00:45 for a 19:00 boundary stamps
			//     19:00:45 and the next 19:00-boundary check on the same
			//     day is satisfied (no double-fire).
			//   - Any future audit consumer that needs "the 19:00 boundary
			//     processed at 19:00" must pass `boundaryAt` into the
			//     command — keeping wall-clock matches the legacy semantics
			//     today's consumers (no one) expect.
			w.Environment.LastTransitionAt = now

			// Determine which objects need to flip BEFORE bumping the
			// generation, so the snapshot we compute is consistent.
			// Carve out TagLamplighterTarget for the lamplighter
			// cascade ONLY when an actor carries AttrLamplighter; if no
			// lamplighter is configured, those objects flip in the bulk
			// pass so they don't get stuck.
			excludeTag := ""
			if hasActorWithAttribute(w, AttrLamplighter) {
				excludeTag = TagLamplighterTarget
			}
			flips := determineTransitionFlips(w, newPhase, excludeTag)

			// Bump generation AFTER the mutation. Anything racing against
			// us via WorldEventGen.Load() will see one of two consistent
			// snapshots — either the pre-transition gen with pre-transition
			// phase, or the post-transition gen with post-transition phase.
			gen := w.WorldEventGen.Add(1)
			for i := range flips {
				flips[i].Gen = gen
			}

			// Schedule the timers. Launches goroutines but returns
			// immediately — the world goroutine is free to handle the
			// next command.
			//
			// TODO(rewrite): when Hub/WS layer ports, broadcast
			// world_phase_changed here so clients start the canvas tween
			// at the boundary.
			//
			// TODO(rewrite): when occupancy ports, refresh night-only
			// structure states here so taverns/inns light up at dusk even
			// if no one is currently entering.
			scheduleFlips(w, flips)

			w.emit(&PhaseApplied{
				At:              now,
				From:            from,
				To:              newPhase,
				Gen:             gen,
				ObjectsAffected: len(flips),
			})

			log.Printf("sim/world_phase: transitioned %s -> %s at %s (gen %d, %d flips scheduled)",
				from, newPhase, now.Format(time.RFC3339), gen, len(flips))

			return PhaseTransitionResult{
				From:            from,
				To:              newPhase,
				At:              now,
				Gen:             gen,
				ObjectsAffected: len(flips),
			}, nil
		},
	}
}

// determineTransitionFlips computes the per-object state changes needed
// to move the world to newPhase. Walks every VillageObject, looks up its
// Asset, picks the AssetState tagged with the target tag (day-active for
// PhaseDay, night-active for PhaseNight); emits a PendingFlip when the
// object's current_state differs from the target state.
//
// When excludeTag is non-empty, any AssetState that ALSO carries
// excludeTag is dropped from the bulk flip — these are objects expected
// to be handled by another mechanism (e.g. lamplighter route).
//
// Gen is left zero by this function; callers stamp it after bumping
// WorldEventGen.
//
// Deterministic ordering: VillageObjects map iteration is randomized in
// Go, so the returned slice is unordered. Callers that need a stable
// order (rare — flip dispatch doesn't care) must sort.
//
// Unexported by design (see buildWalkGrid).
func determineTransitionFlips(w *World, newPhase Phase, excludeTag string) []PendingFlip {
	var tag string
	switch newPhase {
	case PhaseDay:
		tag = TagDayActive
	case PhaseNight:
		tag = TagNightActive
	default:
		return nil
	}

	var flips []PendingFlip
	for id, obj := range w.VillageObjects {
		asset, ok := w.Assets[obj.AssetID]
		if !ok {
			// Object references a missing asset — skip rather than error,
			// matches legacy behavior (orphan rows survived schema churn).
			continue
		}
		target := asset.StateForTag(tag)
		if target == nil {
			continue // this asset isn't phase-sensitive
		}
		if excludeTag != "" && target.HasTag(excludeTag) {
			continue // claimed by another dispatch mechanism
		}
		if obj.CurrentState == target.State {
			continue // already there
		}
		flips = append(flips, PendingFlip{
			ObjectID:      id,
			NewState:      target.State,
			SpreadSeconds: asset.TransitionSpreadSeconds,
		})
	}
	return flips
}

// scheduleFlips launches a goroutine timer for each flip, then returns
// immediately. Flips with SpreadSeconds > 0 fire at a uniform-random
// offset in [0, SpreadSeconds) seconds; SpreadSeconds == 0 fires on a
// fresh goroutine with zero delay (still async, no head-of-line block on
// the world goroutine).
//
// Each fired flip uses SendContext (not Submit) so non-applied results
// can be logged and shutdown unblocks the timer cleanly. If the world has
// moved on (WorldEventGen advanced past Gen), the command returns
// Applied=false / Reason="superseded" — the stale flip evaporates without
// overwriting fresh state. Expected non-applied reasons ("superseded",
// "already_at_target") are silent; anything else is logged so latent
// scheduling bugs surface in ops logs instead of disappearing.
//
// Fire-and-forget: scheduleFlips returns no error, the timer goroutines
// own the rest of the lifecycle. Unexported by design.
func scheduleFlips(w *World, flips []PendingFlip) {
	for _, f := range flips {
		flip := f
		var delay time.Duration
		if flip.SpreadSeconds > 0 {
			delay = time.Duration(mathrand.IntN(flip.SpreadSeconds)) * time.Second
		}
		time.AfterFunc(delay, func() { fireScheduledFlip(w, flip) })
	}
}

// fireScheduledFlip is the body of the time.AfterFunc callback launched
// by scheduleFlips, factored out so the shutdown test can invoke the
// post-shutdown path synchronously without waiting on a random timer
// delay.
//
// Uses the world's lifecycle context (NOT context.Background) so that
// a shutdown-while-timer-armed unblocks the SendContext call instead of
// deadlocking forever on a cmds-channel send to a dead world goroutine.
// Pulled fresh inside the callback because the schedule-to-fire window
// can be many seconds.
func fireScheduledFlip(w *World, flip PendingFlip) {
	ctx := w.LifecycleContext()
	res, err := w.SendContext(ctx, SetVillageObjectState(flip.ObjectID, flip.NewState, flip.Gen))
	if err != nil {
		// Shutdown isn't an error worth shouting about; any other
		// failure is.
		if ctx.Err() == nil {
			log.Printf("sim/world_phase: scheduled flip for %s -> %s failed: %v",
				flip.ObjectID, flip.NewState, err)
		}
		return
	}
	sr, ok := res.(SetStateResult)
	if !ok {
		return
	}
	if sr.Applied {
		return
	}
	switch sr.Reason {
	case "superseded", "already_at_target":
		// Expected — the world moved on or we converged.
	default:
		log.Printf("sim/world_phase: scheduled flip for %s -> %s skipped: %+v",
			flip.ObjectID, flip.NewState, sr)
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
func checkAndTransition(ctx context.Context, w *World) {
	// Snapshot config via the world goroutine so we read consistent
	// dawn/dusk/location values relative to any concurrent settings
	// changes.
	cfgValue, err := w.SendContext(ctx, Command{
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
		if ctx.Err() == nil {
			log.Printf("sim/world_phase: snapshot config failed: %v", err)
		}
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

	if _, err := w.SendContext(ctx, ApplyPhaseTransition(targetPhase)); err != nil {
		if ctx.Err() == nil {
			log.Printf("sim/world_phase: transition to %s failed: %v", targetPhase, err)
		}
	}
}

type phaseTickerConfig struct {
	DawnTime string
	DuskTime string
	Location *time.Location
	Last     time.Time
}
