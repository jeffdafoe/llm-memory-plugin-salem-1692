package handlers

import (
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// RegisterTickHandlers wires a TickWorkerPool into the world as ONE unit:
// it installs the pool as the reactor evaluator's admission controller AND
// registers the pool's ReactorTickDue subscriber.
//
// Both halves are mandatory and must be installed together — that is why
// there is a single bootstrap entry point rather than two exported steps:
//
//   - admission controller WITHOUT the subscriber: the evaluator would
//     consume an actor's warrants and emit a ReactorTickDue that reaches no
//     one. The warrants are gone; the tick never runs.
//   - subscriber WITHOUT the admission controller: the world keeps the
//     default alwaysAdmit, so under load the evaluator out-runs the worker
//     pool, the bounded job channel overflows, and the subscriber panics on
//     its admission-invariant check.
//
// Exposing only this function makes a partial wiring unrepresentable.
//
// Call before World.Run, or from inside a Command.Fn (both run on, or
// before, the world goroutine — see World.Subscribe /
// World.SetTickAdmissionController). Then call Start on the pool to launch
// the workers; Stop + Wait on shutdown.
func RegisterTickHandlers(w *sim.World, pool *TickWorkerPool) {
	if w == nil || pool == nil {
		panic("handlers: RegisterTickHandlers requires a non-nil world and pool")
	}
	// The pool's workers complete ticks against pool.world via SendContext.
	// Wiring it as the admission controller / subscriber of a DIFFERENT
	// world would split the pipeline across two worlds — admission and
	// enqueue on w, completion on pool.world — silently corrupting both.
	if pool.world != w {
		panic("handlers: RegisterTickHandlers — pool was built for a different world")
	}
	w.SetTickAdmissionController(pool)
	w.Subscribe(sim.SubscriberFunc(pool.handleEvent))
}
