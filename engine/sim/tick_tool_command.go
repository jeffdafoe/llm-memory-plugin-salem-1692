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

// TickToolResult is the envelope RunTickToolCommand returns to the off-world
// tick harness: the inner tool command's own result, plus a fresh snapshot of
// the acting actor taken right after the command's Fn ran on the world
// goroutine.
//
// PostActorSnapshot exists so the harness can re-perceive an actor's OWN-state
// perception mid-tick when a non-terminal commit changed it — a consume that
// eased a need and spent stock, a buy that moved coins/goods. Without it the
// tick-open `## You` block and the eat/drink/buy affordances are rendered once
// and re-sent verbatim on every within-tick round, so they keep priming the
// already-satisfied action and the weak model re-fires it (LLM-88: Josiah ate,
// then re-consumed against a still-"you feel thirsty / consume to drink"
// furniture). It is nil when the command failed (no mutation) or the actor
// vanished mid-command; the harness treats nil as "nothing to refresh".
type TickToolResult struct {
	Result            any
	PostActorSnapshot *ActorSnapshot
}

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
			// LLM-209: a NO-OP rejection (the actor asked for something it already
			// has — walk to the structure it already stands in / is already walking
			// to, take a break while already on break) is TERMINAL, not a
			// correctable error. Preserve its concrete type instead of flattening it
			// to ModelFacingError, so the harness dispatch can recognize it via
			// errors.As and END the tick — rather than echoing it non-terminally to
			// be re-fired to the iteration budget (the observed move_to×6 /
			// take_break×6 budget_forced storm).
			var noop TerminalNoOpError
			if errors.As(err, &noop) {
				return TickToolResult{Result: res}, err
			}
			// The tool command's own Fn error is the command validator's
			// model-facing rejection reason — written for the model to read and
			// correct its NEXT call ("no one named X in this conversation", "use a
			// structure_id you can see in your perception"). Tag it as
			// ModelFacingError so the harness echoes it to the LLM. The wrapper's
			// own errors above (actor-not-found, nil command, stale) and
			// newRootedCommand's root-validation error stay UNtagged, so internal
			// dispatch detail never leaks into the prompt — those surface to the
			// model as a generic label instead.
			//
			// A failed command made no mutation, so PostActorSnapshot stays nil
			// (and the harness ignores the result on the error path anyway); the
			// envelope just keeps the success/error returns one type.
			return TickToolResult{Result: res}, ModelFacingError{Msg: err.Error()}
		}
		// LLM-88: capture the acting actor's post-commit self-state alongside the
		// tool's result. The Fn just ran on the world goroutine, so a re-read of
		// the actor reflects any need/inventory/coin change it made; snapshotActor
		// is the same builder the published snapshot uses, so perception consumes
		// it unchanged. Re-read rather than reuse the pre-Fn `actor` pointer in
		// case the command replaced or removed the entry.
		var post *ActorSnapshot
		if a, ok := w.Actors[actorID]; ok {
			post = snapshotActor(a, w.TickCounter, w.Settings.degeneracyEnabled())
		}
		return TickToolResult{Result: res, PostActorSnapshot: post}, nil
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

// TerminalNoOpError marks an AGENT-TICK COMMAND no-op that should END THE
// CURRENT TICK: the actor asked for something it already has — walk to the
// structure it is already in (or already walking to), take a break while already
// on break. The message is model-facing like ModelFacingError, but unlike a
// correctable rejection there is nothing for the model to FIX by retrying — the
// request is already satisfied, so a retry is pure waste. Use it ONLY where the
// terminal semantics are wanted (it ends deliberation for the tick); a rejection
// the model should correct and retry stays a plain error / ModelFacingError.
//
// RunTickToolCommand preserves this type (does NOT flatten it to
// ModelFacingError), so the tick harness can distinguish it via errors.As and
// END the tick on it (a terminal, one-attempt-per-tick outcome) rather than
// echoing it non-terminally. Without this a weak model re-fires the identical
// no-op every round to the iteration budget — the move_to×6 / take_break×6
// budget_forced storm (LLM-209). Compared on type via errors.As, so it survives
// %w wrapping.
type TerminalNoOpError struct{ Msg string }

// Error returns the model-facing reason. The empty-Msg fallback is defensive: a
// zero-value TerminalNoOpError{} (a construction slip) still changes terminal
// behavior, so it must never surface to the model as a bare "[ok] " with no
// reason — give it a sane default instead.
func (e TerminalNoOpError) Error() string {
	if e.Msg == "" {
		return "that action is already done — nothing more to do this turn."
	}
	return e.Msg
}

// NonTerminalNoOpError is the non-terminal sibling of TerminalNoOpError: a
// command that did NOT change the world and wants its Msg echoed to the model
// as an [ok] result, but — unlike TerminalNoOpError — must NOT end the tick.
// The acting NPC keeps its turn and can act again this round. Used for the
// LLM-317 confabulated-"kitchen" move_to no-op: the actor "arrives" without
// moving (it may be about to produce/act), so ending the tick would waste that
// intent. Compared on type via errors.As, so it survives %w wrapping.
type NonTerminalNoOpError struct{ Msg string }

// Error returns the model-facing reason. Empty-Msg fallback mirrors
// TerminalNoOpError — a zero-value construction slip must never surface as a
// bare "[ok] " with no reason.
func (e NonTerminalNoOpError) Error() string {
	if e.Msg == "" {
		return "you are already there — nothing to walk to."
	}
	return e.Msg
}
