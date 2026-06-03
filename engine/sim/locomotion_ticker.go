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
// Per tick, for each actor with a MoveIntent, the ticker RE-PLANS a path
// from the actor's current tile and advances one step. No path is cached
// on MoveIntent — dynamic blockers and displacement all fall out of the
// per-tick replan.
//
// A mover is never in an active huddle. Encounter huddles only form among
// stationary actors (ZBBS-HOME-340 removed the mid-route encounter that
// used to pull a walker into a conversation), and MoveActor leaves any
// huddle before stamping a MoveIntent — but as a belt-and-suspenders the
// ticker also leaves any active huddle before advancing a mover, so the
// invariant holds even if some other path left a walker huddled. The
// earlier "bilateral pause" that instead SKIPPED huddled movers here is
// gone: it could permanently freeze a player (who never re-ticks to
// re-decide) once a passerby was pulled into a huddle mid-walk.
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
// many consecutive ticks, advanceActorLocomotion records a DeadlockEntry
// for the umbilical /deadlocks view and then (ZBBS-HOME-327) walks the
// mover THROUGH the blocking actor — keeping the MoveIntent — rather than
// hard-stopping it. The counter resets to 0 on any successful one-tile
// step (whether direct, via re-plan, or via the walk-through).
//
// The window is still the transient filter it always was — it just gates
// the walk-through now instead of a give-up. 5 ticks at
// LocomotionTickInterval = ~3.3s of standing still before the mover forces
// past (ZBBS-WORK-341 slowed the tick from 200ms to 666.67ms). 3.3s is
// longer than ideal — players visibly watch the NPC pause — but tightening
// below ~2 ticks risks forcing through a genuinely transient passer-by
// (someone walking through a corridor for one tick) who would have cleared
// on their own. Reconsider once /umbilical/deadlocks data names the shape
// of the real failures.
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
				// A committed mover never stays in an active huddle. This is
				// the "walking away ends the conversation" rule MoveActor
				// already applies on its leaveHuddleFirst path; enforcing it
				// here too closes the residual case where some other path
				// (e.g. a direct StartOutdoorHuddle) left a walker huddled.
				// It must be an explicit leave, not a reliance on
				// checkHuddleDriftAfterPositionMutation below: drift only
				// detaches when a step carries the actor OUT of the huddle's
				// scene bound, so a large-radius or unbounded huddle would
				// otherwise keep the mover huddled-while-walking — still
				// "busy" to the rest/sleep fallbacks and the huddle gate, and
				// able to arrive still in the huddle. (ZBBS-HOME-340, replaces
				// the old bilateral-pause skip that froze such a mover.)
				if actorInActiveHuddle(w, actor) {
					leaveCurrentHuddle(w, actor, now)
					// leaveCurrentHuddle emits HuddleLeft, whose subscribers run
					// synchronously and could (like any emit in this loop — see
					// the top-of-iteration re-fetch) clear/supersede the
					// MoveIntent or remove the actor. Re-validate before
					// advancing so advanceActorLocomotion's non-nil-MoveIntent
					// contract still holds.
					actor = w.Actors[id]
					if actor == nil || actor.MoveIntent == nil {
						continue
					}
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

	// Drift auto-leave for stationary displacement: detaches an actor from
	// a huddle when its scene's bound no longer contains it. For a ticker
	// mover this is a no-op — the explicit leave above already cleared any
	// active huddle before we advanced — but it stays as the designated
	// locomotion drift callsite for the invariant the PR 4a helper enforces
	// (e.g. a future displacement that mutates position without a leave).
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
// increments and at DeadlockStuckThreshold consecutive ticks it records a
// DeadlockEntry on World.DeadlockSnapshot for the umbilical /deadlocks view
// and then walks THROUGH the blocking actor (ZBBS-HOME-327) rather than
// hard-stopping — a stably-blocked mover squeezes past instead of freezing.
// A member's own occupied door short-circuits to an immediate walk-through
// earlier (ZBBS-HOME-348), skipping the stuck window.
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

	// Defensive: every tail path below (HOME-348's immediate door step, the
	// stuck-tick increment, and HOME-327's walk-through) dereferences
	// actor.MoveIntent. The caller guarantees it non-nil and nothing between
	// here and function entry clears it — the only nil-set is finishArrival in
	// the reroute-success branch, which returns. The guard makes that invariant
	// explicit at this recovery path so a future emit-before-threshold can't
	// turn a stale nil into a ticker panic (code_review, ZBBS-HOME-327).
	if actor.MoveIntent == nil {
		return
	}

	// Last-resort door walk-through (ZBBS-HOME-348). The reroute has
	// exhausted with no advanceable detour. If the tile we're blocked on is
	// the StructureEnter goal ITSELF (the door), step onto it anyway even
	// though another actor occupies it.
	//
	// Why this case and no other: structureEntryTile resolves a
	// StructureEnter to exactly one tile — the single reachable interior
	// tile under the shared-identity bridge (the door). A whole household
	// shares one HomeStructureID and funnels through that one tile, so a
	// resident parked or sleeping ON the door locks every other family
	// member out of their own home permanently (replanFailed: masking the
	// sole goal tile leaves no path). v1 never hit this — it had no
	// actor-actor collision at all, so actors freely overlapped on the door.
	// v2's soft-block collision (ZBBS-WORK-340) regressed exactly this
	// interaction. We recover the v1 invariant narrowly: overlap is allowed,
	// but only on the door tile, only for a member's own StructureEnter, and
	// only as a true last resort after the reroute fails. Corridor /
	// visitor-slot / Position deadlocks keep v2's collision untouched.
	//
	// Three conjuncts gate it:
	//   - occupiedNext.Equal(target): occupiedNext is the soft-blocked next
	//     tile, target is the resolved goal. Equality means the mover is
	//     adjacent to the door and the door itself is what's occupied — a
	//     single legal step onto walkable terrain (the soft classification
	//     already proved it's an actor block, not a wall/footprint).
	//   - structureMembershipAllows: the SAME predicate behind the owner-only
	//     gate in resolvePathTarget. Re-checking it here keeps the "member's
	//     own door" scope LOCAL and self-evident instead of leaning on an
	//     upstream resolve 150 lines away — defense-in-depth against a future
	//     routing change that hands a non-member a StructureEnter target. A
	//     non-member targeting an OPEN structure whose single door is occupied
	//     still deadlocks; that's out of scope (the bug is household lockout,
	//     not public-building contention).
	if dest.Kind == MoveDestinationStructureEnter && dest.StructureID != nil &&
		occupiedNext.Equal(target) &&
		structureMembershipAllows(w, actor, *dest.StructureID, now) {
		from := actor.Pos
		fromStructure := actor.InsideStructureID
		actor.Pos = target
		updateInsideStructureIDFromTileOwnership(w, actor)
		actor.MoveIntent.StuckTicks = 0

		w.emit(&ActorMoved{
			ActorID:           actor.ID,
			FromPosition:      from,
			ToPosition:        target,
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

	// Walk-through, not give-up (ZBBS-HOME-327). The mover has been stably
	// soft-blocked for the full window — an actor that isn't going to yield (a
	// sleeping occupant on the route, a head-on mutual block, a non-member at an
	// open structure's busy door). The pre-HOME-327 behavior hard-stopped here
	// (MoveStoppedDeadlocked + cleared MoveIntent), which froze the mover: it
	// abandoned the walk, its needs climbed, and the agent layer just re-decided
	// the same blocked move next tick — the "village looks dead" loop (the live
	// Josiah→Tavern case: ten approaches to a tavern whose keeper slept across
	// the only door). Instead, step THROUGH the blocker — onto occupiedNext, the
	// one-tile-toward-goal step the reroute kept rejecting — and KEEP the
	// MoveIntent so the walk continues. Jeff's directive (2026-05-29): actors are
	// never a permanent obstacle to each other; "two people jamming past each
	// other in a doorway." The deadlock entry above stays as the contention
	// canary (the umbilical /deadlocks view + the ReplanFailed sleeper-vs-clog
	// distinction); only the OUTCOME changed from frozen to squeezed-past.
	//
	// Safe by construction:
	//   - occupiedNext was soft-classified by advanceActorLocomotion
	//     (classifyTileBlocker): walkable terrain with an actor on it, NEVER a
	//     wall, water, footprint, or closed-structure tile (those classify HARD
	//     and stop the move earlier). The step is one legal terrain tile — the
	//     mover never clips into a building.
	//   - Entry policy is already enforced upstream: resolvePathTarget refuses a
	//     StructureEnter target for a closed / non-member-owner-only structure,
	//     so a mover walking toward `target` is authorized to be there. Stepping
	//     through the blocker bypasses no access rule. (HOME-348's member-only
	//     immediate door walk-through still fires earlier for a member's own
	//     door; this is the general last resort for everyone else.)
	//   - Last resort: the reroute-around loop ran first, and the stuck window
	//     confirmed the block is stable rather than a one-tick brush-past.
	from := actor.Pos
	fromStructure := actor.InsideStructureID
	actor.Pos = occupiedNext
	updateInsideStructureIDFromTileOwnership(w, actor)
	actor.MoveIntent.StuckTicks = 0

	w.emit(&ActorMoved{
		ActorID:           actor.ID,
		FromPosition:      from,
		ToPosition:        occupiedNext,
		FromStructureID:   fromStructure,
		ToStructureID:     actor.InsideStructureID,
		MovementAttemptID: attemptID,
		At:                now,
	})

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
		// Chebyshev radius, NOT ring-membership: pickVisitorSlotAtPin can park a
		// visitor on the pin tile itself (Chebyshev 0) as a last resort when all
		// eight ring slots are blocked. A ring-only check would never register
		// that arrival → finishArrival never runs → the mover loops on the pin
		// forever. LoiterAttributionTiles (1) covers the pin (0) and all eight
		// ring slots (1), matching the ObjectVisit arm below. ZBBS-HOME-329.
		return pin.Chebyshev(actor.Pos) <= LoiterAttributionTiles
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

	// Resolve the DESTINATION the mover walked to, not the structure it is
	// physically inside: a StructureVisit/knock arrives at a loiter slot
	// OUTSIDE the shop (InsideStructureID == ""), and an ObjectVisit at a
	// well/tree is not a structure at all. dest carries the real target.
	// Carried on both the ActorArrived event (so the action-log walked entry
	// names the place, ZBBS-WORK-359) and the arrival warrant (so perception
	// renders "You arrived at <the shop / the well>", ZBBS-WORK-358).
	var destStructure StructureID
	if dest.StructureID != nil {
		destStructure = *dest.StructureID
	}
	var destObject VillageObjectID
	if dest.ObjectID != nil {
		destObject = *dest.ObjectID
	}

	arrivedEvt := &ActorArrived{
		ActorID:           actor.ID,
		FinalPosition:     finalPos,
		FinalStructureID:  finalStructure,
		DestStructureID:   destStructure,
		DestObjectID:      destObject,
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
				AtStructureID: destStructure,
				AtObjectID:    destObject,
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
// that has not concluded. Used by the rest/sleep fallbacks and the
// StartOutdoorHuddle participant gate to avoid acting on an actor who is
// mid-conversation. (The locomotion ticker no longer consults it — the
// old bilateral pause was removed in ZBBS-HOME-340.)
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

	// Authoritative inside-state push for the client (ZBBS-WORK-373): the v2
	// analog of v1's setNPCInside broadcast, restoring the npc_inside_changed
	// frame the client's apply_npc_inside_change handler still consumes. The
	// unchanged-value early return above guarantees this fires only on a real
	// flip; emitted last, after indices + room + occupancy are consistent.
	w.emit(&ActorInsideChanged{ActorID: actor.ID, InsideStructureID: structureID})
}
