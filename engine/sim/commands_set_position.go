package sim

import (
	"fmt"
	"time"
)

// commands_set_position.go — ZBBS-HOME-448. The umbilical set-position
// command: an operator teleport that displaces an actor to a walkable
// tile without a walk. The recovery lever for stranded/limbo actors (an
// actor parked on a structure footprint the pathfinder can't escape, a
// marooned huddle member) that previously needed a full engine restart.
//
// Deliberately a proper world Command, not a bare field write: direct DB
// writes on engine-owned tables are resurrected by the shutdown
// checkpoint, and a naive position write would leave MoveIntent /
// inside-structure attribution / huddle membership inconsistent.

// ErrTileNotWalkable is returned when the set-position target tile is
// out of bounds or not walkable on the current walk grid (water, a
// structure footprint, an obstacle). Refusing unwalkable targets keeps
// the teleport from recreating the exact stranded-actor pathology it
// exists to fix. Door tiles ARE walkable (buildWalkGrid carves them), so
// "teleport inside" is expressed as a teleport to the door.
var ErrTileNotWalkable = fmt.Errorf("target tile is not walkable")

// SetActorPositionResult is the typed reply from SetActorPosition.
type SetActorPositionResult struct {
	ActorID           ActorID
	From              Position
	To                Position
	InsideStructureID StructureID // post-teleport attribution; empty = outdoors
	MoveCancelled     bool        // an in-flight walk was cancelled
	LeftHuddleID      HuddleID    // huddle the teleport removed the actor from; empty = none
}

// SetActorPosition returns a Command that teleports the actor to the
// target tile. The mutation follows the same recipe as the locomotion
// ticker's walk-through displacement (position write → tile-ownership
// reconciliation → huddle-drift guard), so every derived index moves
// with the position:
//
//  1. Target must be in bounds and walkable on the current grid —
//     ErrTileNotWalkable otherwise. Actor-occupancy is NOT checked:
//     actors are never hard blockers in v2 (ZBBS-HOME-327), and a
//     transient overlap resolves on the next walk.
//  2. An in-flight MoveIntent is cancelled with the standard
//     ActorMoveStopped{cancelled} emit (the same frame StopMove sends),
//     which also lets the route cascade abandon any active NPC route on
//     the actor (handleActorMoveStoppedAdvanceRoute).
//  3. InsideStructureID is reconciled from tile ownership — teleporting
//     onto a door tile attributes the actor inside (emitting the
//     npc_inside_changed flip); teleporting to open ground clears it.
//  4. ActorTeleported is emitted for the client snap (npc_arrived frame).
//  5. checkHuddleDriftAfterPositionMutation removes the actor from a
//     huddle it was displaced away from, with the normal leave/conclude
//     side effects — the drift guard's documented admin-teleport case.
//
// MUST be invoked on the world goroutine (callers go through
// w.SendContext).
func SetActorPosition(actorID ActorID, target Position, now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			actor, ok := w.Actors[actorID]
			if !ok {
				return nil, ErrActorNotFound
			}

			grid, err := buildWalkGrid(w)
			if err != nil {
				return nil, fmt.Errorf("build walk grid: %w", err)
			}
			if !grid.CanWalk(target.X, target.Y) {
				return nil, fmt.Errorf("%w: (%d,%d)", ErrTileNotWalkable, target.X, target.Y)
			}

			moveCancelled := false
			if actor.MoveIntent != nil {
				dest := actor.MoveIntent.Destination
				attemptID := actor.MoveIntent.AttemptID
				actor.MoveIntent = nil
				emitMoveStopped(w, actor, dest, MoveStoppedCancelled, attemptID, now)
				moveCancelled = true
			}

			from := actor.Pos
			actor.Pos = target
			updateInsideStructureIDFromTileOwnership(w, actor)

			w.emit(&ActorTeleported{
				ActorID:           actor.ID,
				FromPosition:      from,
				ToPosition:        target,
				InsideStructureID: actor.InsideStructureID,
				At:                now,
			})

			var leftHuddle HuddleID
			if left := checkHuddleDriftAfterPositionMutation(w, actor.ID, now); len(left) > 0 {
				leftHuddle = left[0]
			}

			return SetActorPositionResult{
				ActorID:           actor.ID,
				From:              from,
				To:                target,
				InsideStructureID: actor.InsideStructureID,
				MoveCancelled:     moveCancelled,
				LeftHuddleID:      leftHuddle,
			}, nil
		},
	}
}
