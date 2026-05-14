package sim

import (
	"errors"
	"fmt"
)

// ErrTickAttemptStale is returned by a RunTickToolCommand command when the
// tick attempt the tool call belongs to is no longer the actor's current
// in-flight attempt. The wrapped tool command is NOT run — a stale worker
// must never mutate the world. Callers (PR 3's harness loop) detect this
// with errors.Is and terminate the tick stale.
var ErrTickAttemptStale = errors.New("sim: tick attempt is stale; tool command not run")

// RunTickToolCommand wraps a world-mutating tool command produced by PR 3's
// off-world tick worker so it (a) runs under the originating tick's cascade
// root and (b) is rejected if the attempt has gone stale.
//
// This is the only correct path for a worker to commit a tool call. The
// worker reads a snapshot off the world goroutine, decides on a tool call,
// then sends the resulting command back via World.SendContext — but by the
// time that command reaches the world goroutine the attempt may have been
// superseded. The guard runs ON the world goroutine against LIVE actor
// state, so it cannot race:
//
//   - actor missing                  → error, tool not run
//   - attempt not the current one    → ErrTickAttemptStale, tool not run
//   - attempt current                → tool.Fn runs, under the inherited root
//
// The inherited-root half is delegated to newRootedCommand: World.Run runs
// the whole handler under withRoot(rootEventID, ...), so events the tool
// command emits continue the tick's cascade rather than starting a fresh
// root. rootEventID must be a real prior event (newRootedCommand validates
// root != 0 && root <= eventSeq) — for a tick that is the ReactorTickDue
// event's own ID.
//
// Only tool.Fn is used; any Reply channel on the inner command is ignored —
// the reply belongs to the command this returns.
func RunTickToolCommand(actorID ActorID, attemptID TickAttemptID, rootEventID EventID, tool Command) Command {
	return newRootedCommand(rootEventID, func(w *World) (any, error) {
		actor, ok := w.Actors[actorID]
		if !ok {
			return nil, fmt.Errorf("sim: RunTickToolCommand: actor %q not found", actorID)
		}
		// Same guard shape as CompleteReactorTick: TickInFlight + non-empty
		// attemptID matter because the zero value of TickAttemptID is also
		// "", so without them a zero-attempt call would match an idle actor.
		if !actor.TickInFlight || attemptID == "" || actor.TickAttemptID != attemptID {
			return nil, ErrTickAttemptStale
		}
		// A zero/nil tool command is a harness bug — fail the tick, never
		// crash the world goroutine on a nil Fn dereference.
		if tool.Fn == nil {
			return nil, fmt.Errorf("sim: RunTickToolCommand: nil tool command for actor %q", actorID)
		}
		return tool.Fn(w)
	})
}
