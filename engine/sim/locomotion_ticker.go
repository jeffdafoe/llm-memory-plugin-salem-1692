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
// at the sub-second cadence: a plain time.Ticker would backlog commands
// if the world goroutine stalls; the self-rearm only schedules the next
// tick after the current one's Fn returns.
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

// LocomotionTickInterval is the locomotion ticker cadence — and, because
// the ticker advances ONE tile per tick, also fixes the visible walk
// speed at TileSize/Interval. 2/3 of a second per tile = 1.5 tiles/sec;
// at today's TileSize=32px that is 48 world-pixels/sec, restoring v1's
// defaultNPCSpeed (v1 engine/npc_movement.go: defaultNPCSpeed = 48.0
// px/s). TestLocomotionPace_MatchesV1Speed pins the relationship so a
// future tile-size or cadence change can't silently re-introduce the
// 3.33x speed-up.
//
// PR 4 originally picked 200ms (160 px/s, ~3.33x v1) on architectural
// symmetry with the reactor evaluator without back-checking against v1's
// effective speed — the rewrite swapped from v1's "server emits a one-
// shot path, client interpolates at server-set speed" model to v2's
// "server ticks and emits one ActorMoved per tile" model, which ties
// speed to cadence mechanically. The change went unnoticed because no
// player was at the keyboard until first live boot tonight (2026-05-27),
// when both PC and NPC walks read as much too fast (ZBBS-WORK-341). The
// fix is to slow the cadence back to v1's effective pace; the client's
// LOCOMOTION_TICK_SECONDS moves in lockstep so visual interpolation
// stays aligned with engine arrival.
//
// Future: lifting per-actor speed into an Actor field (so children walk
// faster than elders, mounted characters faster than walkers, etc.)
// would require decoupling visible speed from tick cadence — either
// fractional-tile-per-tick accumulators or a return to v1's emit-path-
// once model. Out of scope for ZBBS-WORK-341; the const stays the
// single tunable.
const LocomotionTickInterval = 2 * time.Second / 3

// DeadlockStuckThreshold is the per-MoveIntent stuck-tick budget
// (ZBBS-WORK-340). Each tick that the mover soft-blocks AND the re-plan
// with the occupant tile masked off also fails to find an advanceable
// next tile, MoveIntent.StuckTicks is incremented. When it reaches this
// many consecutive ticks, advanceActorLocomotion hard-stops the mover
// with MoveStoppedDeadlocked and records a DeadlockEntry for the
// umbilical /deadlocks view. The counter resets to 0 on any successful
// one-tile step (whether direct or via re-plan).
//
// 5 ticks at LocomotionTickInterval = ~3.3s of wedge before the engine
// reports it (ZBBS-WORK-341 slowed the tick from 200ms to 666.67ms).
// 3.3s is longer than ideal — players visibly watch the NPC stand
// still — but tightening below ~2 ticks risks false-positives on
// genuinely transient passers-by (someone walking through a corridor
// for one tick). Reconsider once /umbilical/deadlocks data names the
// shape of the real failures.
const DeadlockStuckThreshold = 5

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
	w.beatTicker("locomotion")
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

	path := FindPath(grid, actor.Pos, GridPoint{X: target.X, Y: target.Y})
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
		// Another actor is on the next tile. The naive "retry next tick"
		// posture stalls forever when the occupant is sleeping (no path
		// past) or when two movers face each other on adjacent tiles
		// (each is the other's next tile), so we first try to plan AROUND
		// the occupant — most layouts have a detour, which breaks the
		// mutual-block case in one tick. When even the re-plan can't
		// advance, count toward the per-MoveIntent stuck-tick cap; at
		// DeadlockStuckThreshold consecutive ticks we hard-stop with
		// MoveStoppedDeadlocked and record the entry for the umbilical
		// /deadlocks view. See ZBBS-WORK-340.
		advanceActorViaReroute(w, actor, dest, attemptID, grid, path[1], target, now)
		return
	}

	// Advance one tile.
	from := actor.Pos
	fromStructure := actor.InsideStructureID
	actor.Pos = nextTile
	updateInsideStructureIDFromTileOwnership(w, actor)
	actor.MoveIntent.StuckTicks = 0

	w.emit(&ActorMoved{
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

// maxReroutePathsPerTick bounds the iterative widening of the masked-tile
// set in advanceActorViaReroute. Each pass adds at most one tile to the
// mask, so 4 attempts handles a fully-saturated 4-neighborhood — the
// degenerate case where every cardinal neighbor of the mover is occupied
// by a separate actor. Bigger is wasted work: the mover physically
// cannot move if all four neighbors are blocked.
const maxReroutePathsPerTick = 4

// advanceActorViaReroute is the soft-block branch of advanceActorLocomotion
// (ZBBS-WORK-340). It iteratively re-plans with a growing set of masked
// tiles — first the immediate occupant, then any further soft-blocked
// alt-path next tiles A* offers — until either a detour with a clear next
// step is found (mover advances, stuck counter resets) or the mask is
// exhausted (no detour exists at all, OR every detour is itself
// occupied). On exhaustion the mover's per-MoveIntent stuck counter
// increments and at DeadlockStuckThreshold consecutive ticks the mover
// hard-stops with MoveStoppedDeadlocked + a DeadlockEntry on
// World.DeadlockSnapshot for the umbilical /deadlocks view.
//
// The iterative mask is load-bearing: FindPath is deterministic, so if
// the first re-plan returns a detour with a soft-blocked next tile, the
// NEXT tick's re-plan (with only the original occupant masked) returns
// the same detour and gets stuck on the same secondary blocker. Widening
// the mask within one tick lets the planner offer a different detour
// when one exists.
//
// Two failure modes are recorded separately via the ReplanFailed flag on
// the entry: no detour exists at all (the sleeping-Abraham-in-the-only-
// doorway shape; the very first FindPathBlocking — with just the original
// occupant masked — returned nil) vs. detours exist but their first
// tiles are repeatedly occupied (mutual block or clogged corridor; the
// mask widened across multiple passes, each yielding an altSoft, until
// the planner had nothing left to offer). NPC behavior trees and the
// operator surface use the distinction.
//
// occupiedNext is path[1] from the just-rejected straight-line plan — the
// tile classifyTileBlocker soft-blocked on. target is the goal grid point
// resolvePathTarget already produced. Caller guarantees actor.MoveIntent
// is non-nil.
//
// MUST be called from inside a Command.Fn.
func advanceActorViaReroute(w *World, actor *Actor, dest MoveDestination, attemptID MovementAttemptID, grid *WalkGrid, occupiedNext Position, target Position, now time.Time) {
	masked := make([]GridPoint, 0, maxReroutePathsPerTick)
	masked = append(masked, GridPoint{X: occupiedNext.X, Y: occupiedNext.Y})

	replanFailed := false
	for attempt := 0; attempt < maxReroutePathsPerTick; attempt++ {
		altPath := FindPathBlocking(grid, actor.Pos, GridPoint{X: target.X, Y: target.Y}, masked)
		if altPath == nil || len(altPath) < 2 {
			// No path exists given the current mask. The interpretation
			// depends on whether the mask widened past the original
			// occupant: a nil on attempt 0 means no detour exists at all
			// (sleeping-Abraham-in-the-only-doorway), but a nil on a later
			// pass means every detour the planner could offer had its
			// first step occupied, until the mask saturated the cardinal
			// neighborhood (mutual block / clogged corridor). The two
			// shapes are operationally different — only the first is
			// terminal until the occupant moves.
			replanFailed = attempt == 0
			break
		}

		altNext := Position{X: altPath[1].X, Y: altPath[1].Y}
		altHard, altSoft := classifyTileBlocker(grid, w, altNext, actor.ID)
		if altHard {
			// A non-actor blocker on the alt path's first tile (e.g. a
			// building footprint exposed by the mask widening). Treat as
			// "no usable detour this tick".
			break
		}
		if !altSoft {
			// Detour exists and its first step is clear — advance one tile
			// onto altNext. Mirrors the straight-line success path in
			// advanceActorLocomotion.
			from := actor.Pos
			fromStructure := actor.InsideStructureID
			actor.Pos = altNext
			updateInsideStructureIDFromTileOwnership(w, actor)
			actor.MoveIntent.StuckTicks = 0

			w.emit(&ActorMoved{
				ActorID:           actor.ID,
				FromPosition:      from,
				ToPosition:        altNext,
				FromStructureID:   fromStructure,
				ToStructureID:     actor.InsideStructureID,
				MovementAttemptID: attemptID,
				At:                now,
			})

			checkHuddleDriftAfterPositionMutation(w, actor.ID, now)

			if arrivedAtDestination(w, actor, dest) {
				finishArrival(w, actor, dest, attemptID, now)
			}
			return
		}

		// altNext is soft-blocked too. Widen the mask and try again.
		masked = append(masked, GridPoint{X: altNext.X, Y: altNext.Y})
	}

	// No advanceable next-tile this tick. Count toward the stuck-tick cap;
	// the MoveIntent is preserved so the next tick re-plans afresh — an
	// occupant moving off the tile in the meantime resolves it.
	actor.MoveIntent.StuckTicks++
	if actor.MoveIntent.StuckTicks < DeadlockStuckThreshold {
		return
	}

	// Stuck for the full window. Identify the occupant at record time
	// (best-effort — they may have moved off the tile between the soft-
	// block classification and the cap trip; an empty OccupantID/Name on
	// the entry just means "we couldn't name them," which is fine for the
	// diagnostic view).
	var occupantID ActorID
	var occupantName string
	for id, a := range w.Actors {
		if id == actor.ID {
			continue
		}
		if a.Pos.Equal(occupiedNext) {
			occupantID = id
			occupantName = a.DisplayName
			break
		}
	}
	kind, destSID, destOID, destPos := destToView(dest)
	w.RecordDeadlock(DeadlockEntry{
		Time:            now,
		MoverID:         actor.ID,
		MoverName:       actor.DisplayName,
		MoverPos:        actor.Pos,
		DestinationKind: kind,
		DestStructureID: destSID,
		DestObjectID:    destOID,
		DestPosition:    destPos,
		OccupantID:      occupantID,
		OccupantName:    occupantName,
		OccupantTile:    occupiedNext,
		ReplanFailed:    replanFailed,
	})
	emitMoveStopped(w, actor, dest, MoveStoppedDeadlocked, attemptID, now)
	actor.MoveIntent = nil
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
//   - ObjectVisit — the actor stands within LoiterAttributionTiles
//     (Chebyshev) of the object's loiter pin. Unlike StructureVisit this
//     checks the attribution RADIUS, not ring membership, because
//     pickObjectVisitorSlot can park the actor on the pin tile itself
//     (Chebyshev 0) as a last resort when all eight ring slots are blocked.
//     It is the same radius resolveLoiteringObject uses for
//     object-refresh-on-arrival, so a tile accepted here is eligible for
//     refresh attribution — but when several objects overlap in range,
//     attribution follows resolveLoiteringObject's tie-break and may credit
//     a different in-range object than the destination.
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
			if actor.Pos.X == pin.X+off.X && actor.Pos.Y == pin.Y+off.Y {
				return true
			}
		}
		return false
	case MoveDestinationObjectVisit:
		if dest.ObjectID == nil {
			return false
		}
		pin, ok := effectiveObjectLoiterTile(w, *dest.ObjectID)
		if !ok {
			return false
		}
		return pin.Chebyshev(actor.Pos) <= LoiterAttributionTiles
	case MoveDestinationPosition:
		return dest.Position != nil &&
			actor.Pos.X == dest.Position.X &&
			actor.Pos.Y == dest.Position.Y
	default:
		return false
	}
}

// finishArrival clears the MoveIntent, emits ActorArrived, and stamps an
// ArrivalWarrantReason on the mover so the agent layer (PR 3) can react
// to "you have arrived." Caller guarantees actor.MoveIntent is non-nil.
//
// The emit precedes the stamp so the warrant carries full PR 3a source
// lineage (SourceEventID / RootEventID populated from the ActorArrived
// event); the alternative "stamp first, emit after" produces a warrant
// with zero lineage that bypasses source-aware dedup.
func finishArrival(w *World, actor *Actor, dest MoveDestination, attemptID MovementAttemptID, now time.Time) {
	actor.MoveIntent = nil
	finalPos := actor.Pos
	finalStructure := actor.InsideStructureID

	arrivedEvt := &ActorArrived{
		ActorID:           actor.ID,
		FinalPosition:     finalPos,
		FinalStructureID:  finalStructure,
		MovementAttemptID: attemptID,
		At:                now,
	}
	w.emit(arrivedEvt)
	// Arrival-warrant suppression hook (ZBBS-HOME-311). An active summon-
	// errand participant (notably the summoner, a VA NPC) must not LLM-tick
	// on arrival and wander off mid-errand. The hook is the only summon-
	// domain seam in the locomotion ticker — the predicate itself lives in
	// summon.go. nil hook (the default) preserves the unconditional stamp.
	if w.suppressArrivalWarrant == nil || !w.suppressArrivalWarrant(actor) {
		tryStampWarrant(w, actor, WarrantMeta{
			TriggerActorID: actor.ID,
			SourceEventID:  arrivedEvt.EventID(),
			RootEventID:    arrivedEvt.RootEventID(),
			SourceActorID:  actor.ID,
			OccurredAt:     now,
			Reason: ArrivalWarrantReason{
				AttemptID:     attemptID,
				AtStructureID: finalStructure,
				AtPosition:    finalPos,
			},
		}, now)
	}
}

// emitMoveStopped emits ActorMoveStopped for an accepted-but-failed
// attempt. The destination is deep-copied so the event owns its pointer
// fields rather than aliasing the MoveIntent the caller is about to nil.
func emitMoveStopped(w *World, actor *Actor, dest MoveDestination, reason MoveStoppedReason, attemptID MovementAttemptID, now time.Time) {
	w.emit(&ActorMoveStopped{
		ActorID:           actor.ID,
		Position:          actor.Pos,
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
	pos := actor.Pos
	if sid, ok := structureContainingTile(w, pos); ok {
		setActorInsideStructure(w, actor, sid)
		return
	}
	setActorInsideStructure(w, actor, "")
}

// setActorInsideStructure sets actor.InsideStructureID AND the
// actorsByStructure + outdoorActors secondary indices together — the
// three must move as one, since CreateScene reads actorsByStructure for
// participant capture and the encounter subscribers iterate
// outdoorActors. A no-op when the actor is already attributed to
// structureID.
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
	} else {
		delete(w.outdoorActors, actor.ID)
	}
	actor.InsideStructureID = structureID
	if structureID != "" {
		if w.actorsByStructure[structureID] == nil {
			w.actorsByStructure[structureID] = make(map[ActorID]struct{})
		}
		w.actorsByStructure[structureID][actor.ID] = struct{}{}
	} else {
		w.outdoorActors[actor.ID] = struct{}{}
	}

	// A room belongs to exactly one structure, so an InsideRoomID set while the
	// actor was in the structure it just left is now dangling. Clear it unless
	// the room belongs to the structure being entered; a stale cross-structure
	// room ref is the room/structure mismatch that validateActorStructureRefs
	// treats as substrate corruption and hard-fails the next load on.
	if actor.InsideRoomID != 0 {
		if structureID == "" {
			actor.InsideRoomID = 0
		} else if room := findRoom(w, actor.InsideRoomID); room == nil || room.StructureID != structureID {
			actor.InsideRoomID = 0
		}
	}

	// Occupancy: the headcount of both the structure left and the one entered
	// just changed, so recompute their derived occupied/unoccupied visual
	// state. Outdoors ("") has no occupancy. No-op for structures whose asset
	// isn't occupancy-tracked. This is the per-arrival/departure trigger
	// (ZBBS-070) — hooking the single index chokepoint catches every
	// inside-structure change (locomotion, visitor cleanup) the way v1 hooked
	// setNPCInside/Outside.
	if old != "" {
		refreshStructureOccupancyState(w, old)
	}
	if structureID != "" {
		refreshStructureOccupancyState(w, structureID)
	}
}
