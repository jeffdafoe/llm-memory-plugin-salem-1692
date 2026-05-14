package sim

import (
	"context"
	"fmt"
	"log"
	"sort"
	"time"
)

// locomotion_ticker.go — PR 4 locomotion ticker.
//
// RunLocomotionTicker advances every actor that has a MoveIntent one
// tile per tick, on the same coalesced AfterFunc self-rearm shape as the
// PR 2 reactor evaluator (see reactor_evaluator.go). Coalescing matters
// at a 200ms cadence: a plain time.Ticker would backlog commands if the
// world goroutine stalls; the self-rearm only schedules the next tick
// after the current one's Fn returns.
//
// Per tick, for each actor with a MoveIntent that is NOT in an active
// huddle (bilateral pause), the ticker RE-PLANS a path from the actor's
// current tile and advances one step. No path is cached on MoveIntent —
// resume-after-huddle, dynamic blockers, and displacement all fall out
// of the per-tick replan.
//
// Lifecycle (mirrors RunReactorEvaluator):
//
//   RunLocomotionTicker(ctx, w)
//   └─> kickLocomotionTicker            // initial arm via the cmd channel
//        └─> armNextLocomotionTick       // schedules the first AfterFunc
//             └─> [interval] fireScheduledLocomotionTick
//                  └─> SendContext(evaluateLocomotionAndRearm(now))
//                       └─> Fn: clear flag, run scan, re-arm

// LocomotionTickInterval is the locomotion ticker cadence. 200ms gives
// smooth visible movement at a modest per-actor cost. The PR 4 design
// leaves it a const; moving it to WorldSettings is deferred until perf
// tuning surfaces a reason.
const LocomotionTickInterval = 200 * time.Millisecond

// RunLocomotionTicker owns the locomotion ticker's periodic schedule.
// Caller starts this in a goroutine alongside World.Run; it returns when
// ctx is cancelled. Kicks off the first tick immediately so a MoveIntent
// stamped before the ticker started doesn't wait a full interval.
func RunLocomotionTicker(ctx context.Context, w *World) {
	_, err := w.SendContext(ctx, kickLocomotionTicker())
	if err != nil && ctx.Err() == nil {
		log.Printf("sim/locomotion: initial ticker arm failed: %v", err)
	}
	<-ctx.Done()
}

// kickLocomotionTicker returns a Command whose Fn arms the first tick, so
// the initial arm runs on the world goroutine like every other state
// touch rather than poking locomotionTick from RunLocomotionTicker's
// goroutine.
func kickLocomotionTicker() Command {
	return Command{
		Fn: func(w *World) (any, error) {
			armNextLocomotionTick(w)
			return nil, nil
		},
	}
}

// armNextLocomotionTick schedules the next locomotion tick after one
// interval. MUST be called from inside a Command.Fn — touches
// w.locomotionTick.scheduled without coordination.
//
// Coalescing: a no-op when a tick is already scheduled. The flag clears
// at the start of the scheduled Fn (see evaluateLocomotionAndRearm), so
// a re-arm during that Fn queues the next tick rather than no-opping.
func armNextLocomotionTick(w *World) {
	if w.locomotionTick.scheduled {
		return
	}
	w.locomotionTick.scheduled = true
	time.AfterFunc(LocomotionTickInterval, func() { fireScheduledLocomotionTick(w) })
}

// fireScheduledLocomotionTick is the AfterFunc callback body. Factored
// out so the shutdown test can drive the post-shutdown path
// synchronously (same pattern as fireScheduledEvaluation).
//
// Uses LifecycleContext so a shutdown-while-armed unblocks SendContext
// instead of deadlocking on a send to a dead cmds channel.
func fireScheduledLocomotionTick(w *World) {
	ctx := w.LifecycleContext()
	if ctx.Err() != nil {
		// Shutdown raced us. Clearing the flag would race with the world
		// goroutine; fresh worlds come from LoadWorld / NewWorld, so a
		// post-shutdown stale flag has no effect anyway.
		return
	}
	_, err := w.SendContext(ctx, evaluateLocomotionAndRearm(time.Now()))
	if err != nil && ctx.Err() == nil {
		log.Printf("sim/locomotion: scheduled tick failed: %v", err)
	}
}

// evaluateLocomotionAndRearm clears the scheduled flag, runs one
// locomotion scan, and re-arms — all in one Fn on the world goroutine.
// Clearing the flag first means the re-arm starts a fresh chain rather
// than seeing the still-set flag and no-opping.
func evaluateLocomotionAndRearm(now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			w.locomotionTick.scheduled = false
			res, err := EvaluateLocomotion(now).Fn(w)
			armNextLocomotionTick(w)
			return res, err
		},
	}
}

// EvaluateLocomotion returns a Command that advances every moving actor
// one tile. Exposed as a Command (not just an internal Fn) so tests can
// drive ticks deterministically through the command channel without the
// AfterFunc timing chain.
//
// Collecting the movers up front doubles as a cheap pre-scan: an empty
// mover set skips the walk-grid build entirely — the common idle case.
func EvaluateLocomotion(now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			// Collect the movers, then process them in a deterministic
			// ActorID order. Each actor's step is applied immediately and
			// classifyTileBlocker reads live positions, so iteration order
			// affects not just which of two contending actors advances but
			// whether a follower advances at all (it may step into a tile
			// the leader just vacated, or soft-block if the leader hasn't
			// moved yet). Sorting makes that resolution reproducible across
			// ticks and runs; the per-tick replan still re-adapts everyone
			// on the next tick.
			movers := make([]ActorID, 0, len(w.Actors))
			for id, a := range w.Actors {
				if a.MoveIntent != nil {
					movers = append(movers, id)
				}
			}
			if len(movers) == 0 {
				return nil, nil
			}
			sort.Slice(movers, func(i, j int) bool { return movers[i] < movers[j] })

			grid, err := buildWalkGrid(w)
			if err != nil {
				return nil, fmt.Errorf("locomotion tick: build walk grid: %w", err)
			}
			for _, id := range movers {
				actor := w.Actors[id]
				// Re-fetch and re-check: movers was snapshotted at the top
				// of the tick, but processing an earlier actor emits events
				// (subscribers run synchronously) and runs the drift helper,
				// either of which could in principle delete this actor or
				// clear/supersede its MoveIntent before we reach it.
				if actor == nil || actor.MoveIntent == nil {
					continue
				}
				if actorInActiveHuddle(w, actor) {
					continue // bilateral pause — see actorInActiveHuddle
				}
				advanceActorLocomotion(w, actor, grid, now)
			}
			return nil, nil
		},
	}
}

// advanceActorLocomotion runs one locomotion step for a single moving
// actor: re-resolve the target, check for arrival, plan a path, classify
// the next tile, advance (or stop). Caller guarantees actor.MoveIntent
// is non-nil and the actor is not in an active huddle.
//
// MUST be called from inside a Command.Fn.
func advanceActorLocomotion(w *World, actor *Actor, grid *WalkGrid, now time.Time) {
	dest := actor.MoveIntent.Destination
	attemptID := actor.MoveIntent.AttemptID

	// Keep InsideStructureID (and the actorsByStructure index) honest with
	// the actor's current tile before the arrival check — covers an actor
	// placed directly on a structure tile without a prior locomotion step,
	// and reconciles a structure removed out from under a standing actor.
	updateInsideStructureIDFromTileOwnership(w, actor)

	target, ok := resolvePathTarget(w, actor, dest, grid, now)
	if !ok {
		emitMoveStopped(w, actor, dest, MoveStoppedInvalidated, attemptID, now)
		actor.MoveIntent = nil
		return
	}
	if arrivedAtDestination(w, actor, dest) {
		finishArrival(w, actor, dest, attemptID, now)
		return
	}

	path := FindPath(grid, GridPoint{X: actor.CurrentX, Y: actor.CurrentY}, GridPoint{X: target.X, Y: target.Y})
	if path == nil {
		emitMoveStopped(w, actor, dest, MoveStoppedUnreachable, attemptID, now)
		actor.MoveIntent = nil
		return
	}
	if len(path) < 2 {
		// start == target, yet arrivedAtDestination said no. Unreachable in
		// practice (Position / StructureVisit: start==target IS arrival;
		// StructureEnter: the reconcile above flips InsideStructureID when
		// the actor stands on the door tile). Defensive: preserve the
		// intent and retry next tick rather than indexing a 1-element path.
		return
	}

	nextTile := Position{X: path[1].X, Y: path[1].Y}
	hard, soft := classifyTileBlocker(grid, w, nextTile, actor.ID)
	if hard {
		emitMoveStopped(w, actor, dest, MoveStoppedBlocked, attemptID, now)
		actor.MoveIntent = nil
		return
	}
	if soft {
		// Transient occupant — keep the MoveIntent and retry next tick.
		return
	}

	// Advance one tile.
	from := Position{X: actor.CurrentX, Y: actor.CurrentY}
	fromStructure := actor.InsideStructureID
	actor.CurrentX = nextTile.X
	actor.CurrentY = nextTile.Y
	updateInsideStructureIDFromTileOwnership(w, actor)

	w.emit(ActorMoved{
		ActorID:           actor.ID,
		FromPosition:      from,
		ToPosition:        nextTile,
		FromStructureID:   fromStructure,
		ToStructureID:     actor.InsideStructureID,
		MovementAttemptID: attemptID,
		At:                now,
	})

	// Drift auto-leave: a ticker-moved actor is never in an active huddle
	// (the bilateral-pause skip above guarantees it), so this is a no-op
	// today — but it is the designated locomotion drift callsite the PR 4a
	// helper was built for, and it keeps the invariant enforced if a
	// future path lets a moving actor also hold a huddle.
	checkHuddleDriftAfterPositionMutation(w, actor.ID, now)

	if arrivedAtDestination(w, actor, dest) {
		finishArrival(w, actor, dest, attemptID, now)
	}
}

// arrivedAtDestination reports whether the actor has reached dest.
// Destination-kind-aware:
//
//   - StructureEnter — InsideStructureID equals the destination structure
//     (set by updateInsideStructureIDFromTileOwnership when the actor
//     steps onto a footprint tile, i.e. the door).
//   - StructureVisit — the actor stands on ANY of the structure's eight
//     visitor-slot ring tiles. Arrival deliberately checks ring
//     MEMBERSHIP, not the slot pickVisitorSlot currently prefers:
//     pickVisitorSlot excludes tiles occupied by other actors, so
//     re-picking here would un-arrive an actor the instant an
//     earlier-hash-order slot frees up — it would walk off a perfectly
//     good slot to chase the preferred one. Ring membership is stable.
//   - Position — the actor stands on the exact tile.
//
// MUST be called from inside a Command.Fn.
func arrivedAtDestination(w *World, actor *Actor, dest MoveDestination) bool {
	switch dest.Kind {
	case MoveDestinationStructureEnter:
		return dest.StructureID != nil &&
			actor.InsideStructureID != "" &&
			actor.InsideStructureID == *dest.StructureID
	case MoveDestinationStructureVisit:
		if dest.StructureID == nil {
			return false
		}
		pin, ok := effectiveLoiterTile(w, *dest.StructureID)
		if !ok {
			return false
		}
		for _, off := range visitorSlotOffsets {
			if actor.CurrentX == pin.X+off.X && actor.CurrentY == pin.Y+off.Y {
				return true
			}
		}
		return false
	case MoveDestinationPosition:
		return dest.Position != nil &&
			actor.CurrentX == dest.Position.X &&
			actor.CurrentY == dest.Position.Y
	default:
		return false
	}
}

// finishArrival clears the MoveIntent, emits ActorArrived, and stamps an
// ArrivalWarrantReason on the mover so the agent layer (PR 3) can react
// to "you have arrived." Caller guarantees actor.MoveIntent is non-nil.
func finishArrival(w *World, actor *Actor, dest MoveDestination, attemptID MovementAttemptID, now time.Time) {
	actor.MoveIntent = nil
	finalPos := Position{X: actor.CurrentX, Y: actor.CurrentY}
	finalStructure := actor.InsideStructureID

	w.emit(ActorArrived{
		ActorID:           actor.ID,
		FinalPosition:     finalPos,
		FinalStructureID:  finalStructure,
		MovementAttemptID: attemptID,
		At:                now,
	})
	tryStampWarrant(w, actor, WarrantMeta{
		TriggerActorID: actor.ID,
		Reason: ArrivalWarrantReason{
			AttemptID:     attemptID,
			AtStructureID: finalStructure,
			AtPosition:    finalPos,
		},
	}, now)
}

// emitMoveStopped emits ActorMoveStopped for an accepted-but-failed
// attempt. The destination is deep-copied so the event owns its pointer
// fields rather than aliasing the MoveIntent the caller is about to nil.
func emitMoveStopped(w *World, actor *Actor, dest MoveDestination, reason MoveStoppedReason, attemptID MovementAttemptID, now time.Time) {
	w.emit(ActorMoveStopped{
		ActorID:           actor.ID,
		Position:          Position{X: actor.CurrentX, Y: actor.CurrentY},
		StructureID:       actor.InsideStructureID,
		Destination:       cloneMoveDestination(dest),
		Reason:            reason,
		MovementAttemptID: attemptID,
		At:                now,
	})
}

// actorInActiveHuddle reports whether the actor is currently in a huddle
// that has not concluded. The locomotion ticker skips these actors —
// bilateral pause: a walking actor pulled into a huddle suspends, and
// resumes (next tick) once they leave.
func actorInActiveHuddle(w *World, actor *Actor) bool {
	if actor.CurrentHuddleID == "" {
		return false
	}
	h, ok := w.Huddles[actor.CurrentHuddleID]
	return ok && h.ConcludedAt == nil
}

// updateInsideStructureIDFromTileOwnership reconciles actor.InsideStructureID
// (and the actorsByStructure index) with the actor's current tile: the
// actor is "inside" whichever structure's footprint contains its tile, or
// no structure if its tile is outside every footprint.
//
// MUST be called from inside a Command.Fn.
func updateInsideStructureIDFromTileOwnership(w *World, actor *Actor) {
	pos := Position{X: actor.CurrentX, Y: actor.CurrentY}
	if sid, ok := structureContainingTile(w, pos); ok {
		setActorInsideStructure(w, actor, sid)
		return
	}
	setActorInsideStructure(w, actor, "")
}

// setActorInsideStructure sets actor.InsideStructureID AND the
// actorsByStructure secondary index together — the two must move as one,
// since CreateScene reads actorsByStructure for participant capture. A
// no-op when the actor is already attributed to structureID.
//
// MUST be called from inside a Command.Fn.
func setActorInsideStructure(w *World, actor *Actor, structureID StructureID) {
	old := actor.InsideStructureID
	if old == structureID {
		return
	}
	if old != "" {
		if members, ok := w.actorsByStructure[old]; ok {
			delete(members, actor.ID)
			if len(members) == 0 {
				delete(w.actorsByStructure, old)
			}
		}
	}
	actor.InsideStructureID = structureID
	if structureID != "" {
		if w.actorsByStructure[structureID] == nil {
			w.actorsByStructure[structureID] = make(map[ActorID]struct{})
		}
		w.actorsByStructure[structureID][actor.ID] = struct{}{}
	}
}
