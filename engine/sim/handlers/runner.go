package handlers

import (
	"context"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// tickRunner is the seam between the worker-pool lifecycle (PR 3b) and the
// real turn execution (PR 3c perception + PR 3d harness loop). The pool
// owns dequeue, telemetry, and completion; it delegates the turn itself to
// a runner.
//
// RunTick executes OFF the world goroutine. It may read w.Published() and
// send commands back via w.SendContext / sim.RunTickToolCommand, but must
// never block the world goroutine. It returns the sim.TickResult the pool
// hands to sim.CompleteReactorTick.
//
// The interface is unexported on purpose: the real runner lands in this
// package (PR 3c/3d), so there is no external implementer to support — and
// keeping it in-package lets tests inject fakes via newPoolWithRunner.
type tickRunner interface {
	RunTick(ctx context.Context, w *sim.World, job tickJob) sim.TickResult
}

// stubRunner is the PR 3b placeholder. It does no perception, no LLM call,
// and no tool dispatch — it returns a minimal completion
// (TickStatusUnknown), which sim.CompleteReactorTick treats as "clear the
// attempt, carry nothing, move nothing". That is enough to exercise the
// pool's full lifecycle (admission → enqueue → telemetry → completion)
// before PR 3c/3d replace it with the real harness.
type stubRunner struct{}

func (stubRunner) RunTick(_ context.Context, _ *sim.World, _ tickJob) sim.TickResult {
	return sim.TickResult{TerminalStatus: sim.TickStatusUnknown}
}
