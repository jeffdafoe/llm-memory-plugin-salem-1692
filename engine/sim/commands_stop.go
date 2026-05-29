package sim

import (
	"errors"
	"fmt"
	"time"
)

// commands_stop.go — ZBBS-HOME-338. The `stop` tool's substrate command: a
// voluntary halt of an in-flight walk, the agent-facing counterpart to the
// locomotion ticker's failure stops (blocked / unreachable / invalidated /
// deadlocked). It lets an actor abandon a walk where it stands — e.g. to eat
// or rest now rather than being obliged to finish a walk it no longer wants —
// instead of being trapped in motion until arrival (every non-move action is
// gated on MoveIntent == nil).

// StopMove cancels actorID's in-flight MoveIntent, halting the actor on its
// current tile. It clears the intent and emits ActorMoveStopped{cancelled} so
// the client ends the walk animation cleanly (the same frame the failure stops
// use; emitMoveStopped stamps no warrant, so a voluntary cancel is benign on
// the reactor side). The `stop` tool is advertised only while the actor is
// moving (gateTools), so the not-moving branch is defense in depth for
// non-handler callers (tests, admin paths).
func StopMove(actorID ActorID, now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			actor, ok := w.Actors[actorID]
			if !ok {
				return nil, fmt.Errorf("StopMove: actor %q not in world", actorID)
			}
			if actor.MoveIntent == nil {
				return nil, errors.New("you are not walking — there is nothing to stop.")
			}
			dest := actor.MoveIntent.Destination
			attemptID := actor.MoveIntent.AttemptID
			actor.MoveIntent = nil
			emitMoveStopped(w, actor, dest, MoveStoppedCancelled, attemptID, now)
			return nil, nil
		},
	}
}
