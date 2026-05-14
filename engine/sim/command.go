package sim

import "fmt"

// Command is one operation against the World. The world goroutine processes
// commands one at a time in FIFO order — no other access to World state is
// permitted. The Fn closure runs synchronously inside the world goroutine
// with exclusive access; it must NOT block on I/O (LLM HTTP calls happen
// outside the world goroutine — see World docs).
//
// If Reply is non-nil, the world goroutine sends a CommandResult on it
// after Fn returns. Fire-and-forget commands leave Reply nil.
type Command struct {
	Fn    func(w *World) (any, error)
	Reply chan CommandResult

	// inheritedRoot, when nonzero, is a causal root EventID this command
	// continues — set ONLY by newRootedCommand (the internal cross-boundary
	// hook PR 3's off-world worker uses to keep a tick's tool-call commands
	// under the originating cascade's root). The dispatcher (World.Run) runs
	// the whole handler under withRoot(inheritedRoot, ...) so events emitted
	// by the handler inherit the root and it cannot bleed into the next
	// command. Unexported so external/API commands cannot forge a root.
	inheritedRoot EventID
}

// newRootedCommand builds a Command whose handler runs under the causal
// root `root` — the internal cross-boundary hook for PR 3's off-world
// worker, which sends tool-call commands back that must continue the
// originating tick's cascade root.
//
// This is a trust boundary: external/API commands must not be able to
// forge a causal root, so the hook is internal to the sim package and
// only this helper sets Command.inheritedRoot.
//
// Validation runs inside the Fn (on the world goroutine, where eventSeq
// is readable): root must be nonzero and refer to an event already
// emitted this run (root <= eventSeq). That proves the root is a real
// prior event — it does not prove the ID was itself a root event, but
// that is sufficient given the hook is internal-only (full root-set
// tracking is deferred). A failed check rejects the command with an
// error and fn does not run.
func newRootedCommand(root EventID, fn func(w *World) (any, error)) Command {
	return Command{
		inheritedRoot: root,
		Fn: func(w *World) (any, error) {
			if root == 0 || uint64(root) > w.eventSeq {
				return nil, fmt.Errorf("sim: invalid inherited root event id %d (eventSeq=%d)", root, w.eventSeq)
			}
			return fn(w)
		},
	}
}

// CommandResult is what a command's Fn returns, packaged for the reply
// channel. Value is whatever the Fn produces (typically a snapshot or a
// small outcome struct). Err is non-nil only on command-level rejection
// such as state validation — channel-send failures don't bubble through
// here.
type CommandResult struct {
	Value any
	Err   error
}
