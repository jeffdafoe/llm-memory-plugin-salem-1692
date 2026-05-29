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
		res, err := tool.Fn(w)
		if err != nil {
			// The tool command's own Fn error is the command validator's
			// model-facing rejection reason — written for the model to read and
			// correct its NEXT call ("no one named X in this conversation", "use a
			// structure_id you can see in your perception"). Tag it as
			// ModelFacingError so the harness echoes it to the LLM. The wrapper's
			// own errors above (actor-not-found, nil command, stale) and
			// newRootedCommand's root-validation error stay UNtagged, so internal
			// dispatch detail never leaks into the prompt — those surface to the
			// model as a generic label instead.
			return res, ModelFacingError{Msg: err.Error()}
		}
		return res, nil
	})
}

// ModelFacingError marks an error whose message is safe and intended to be
// shown to the LLM in the tool-result transcript. Command validators reach the
// model through RunTickToolCommand, which tags their Fn errors with this type;
// the tick harness echoes a ModelFacingError's message to the model (so it can
// correct its next call) and renders every other dispatch error as a generic
// label (so stale/actor-not-found/nil-command/invalid-root/context errors never
// leak into the prompt). Compared on type via errors.As, so it survives %w
// wrapping.
type ModelFacingError struct{ Msg string }

func (e ModelFacingError) Error() string { return e.Msg }
