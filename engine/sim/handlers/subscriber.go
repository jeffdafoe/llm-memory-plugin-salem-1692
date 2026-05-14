package handlers

import (
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// handleEvent is the pool's ReactorTickDue subscriber. RegisterTickHandlers
// registers it via World.Subscribe; it runs INLINE on the world goroutine,
// immediately after the evaluator emits.
//
// The enqueue is non-blocking, and the panic on the default branch is
// correct — not paranoia. The evaluator already called CanAdmit() on this
// same goroutine, before consuming the actor's warrants and emitting; only
// the world goroutine sends to p.jobs and only workers drain it, so between
// that CanAdmit and this enqueue the buffer length cannot have risen. If
// the send would block, an admission invariant has been broken (single
// producer / synchronous dispatch / Stop-makes-CanAdmit-false) and must be
// fixed at the source. "Log and continue" here would be worse than a
// crash: it would drop the job AFTER its warrants were consumed, recreating
// the exact consumed-but-never-ran state Option A exists to eliminate.
func (p *TickWorkerPool) handleEvent(_ *sim.World, evt sim.Event) {
	due, ok := evt.(*sim.ReactorTickDue)
	if !ok {
		return
	}
	job := tickJob{
		actorID:     due.ActorID,
		attemptID:   due.AttemptID,
		rootEventID: due.RootEventID(),
		// due.Warrants is the evaluator's private consumed snapshot — the
		// actor's own list was cleared at emit, and the event is discarded
		// after dispatch — so the job can take the slice without copying.
		warrants:       due.Warrants,
		warrantedSince: due.WarrantedSince,
		dueAt:          due.DueAt,
		emittedAt:      due.EmittedAt,
	}
	select {
	case p.jobs <- job:
	default:
		panic("handlers: tick admission invariant violated — CanAdmit was true but the job enqueue would block")
	}
}
