package handlers

import (
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// tickJob is one unit of work for the worker pool — the ReactorTickDue
// subscriber builds one per event and enqueues it.
//
// It MUST carry the consumed warrant batch: warrants are cleared from the
// actor at emit time, so the event's snapshot is the only copy that
// survives. rootEventID is the ReactorTickDue event's own ID — it is its
// own causal root (the evaluator emits with no ambient root), and the
// worker passes it to sim.RunTickToolCommand so every tool call the tick
// commits continues that cascade.
//
// The fields are copied off the event by value (warrants is the event's
// already-private slice — see subscriber.go); the job is owned by the
// worker goroutine that dequeues it.
type tickJob struct {
	actorID        sim.ActorID
	attemptID      sim.TickAttemptID
	rootEventID    sim.EventID
	warrants       []sim.WarrantMeta
	warrantedSince time.Time
	dueAt          time.Time
	emittedAt      time.Time

	// dispatchTick is World.TickCounter at the moment this job was enqueued
	// (captured by the subscriber, which runs inline on the world goroutine
	// during the dispatching command's emit). The dispatching command's
	// post-Fn republish stamps Snapshot.AtTick = dispatchTick+1, so the
	// worker's preflight can tell a snapshot that already reflects this
	// dispatch (AtTick > dispatchTick) from one that simply hasn't caught up
	// yet (AtTick <= dispatchTick). Without it, a worker that reads the
	// published snapshot in the enqueue→republish window sees a pre-dispatch
	// view (TickInFlight=false) and false-classifies the tick Stale. See
	// RunTick's preflight.
	dispatchTick uint64
}
