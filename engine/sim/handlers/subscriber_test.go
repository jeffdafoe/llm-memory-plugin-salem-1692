package handlers

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// subscriber_test.go — the ReactorTickDue subscriber: it enqueues a tickJob
// carrying the consumed warrant batch, ignores other event types, and
// panics rather than silently dropping a job when the buffer is full.

func TestHandleEventEnqueuesJob(t *testing.T) {
	w, tel, cancel := newTestWorld(t, 1)
	defer cancel()
	p := NewTickWorkerPool(w, tel)

	due := &sim.ReactorTickDue{
		ActorID:   "alice",
		AttemptID: "A1",
		Warrants:  []sim.WarrantMeta{{TriggerActorID: "bob"}},
	}
	p.handleEvent(nil, due)

	select {
	case job := <-p.jobs:
		if job.actorID != "alice" || job.attemptID != "A1" {
			t.Fatalf("job carried the wrong identity: %+v", job)
		}
		if len(job.warrants) != 1 || job.warrants[0].TriggerActorID != "bob" {
			t.Fatalf("job did not carry the consumed warrant batch: %+v", job.warrants)
		}
	default:
		t.Fatal("handleEvent did not enqueue a job for a ReactorTickDue event")
	}
}

func TestHandleEventIgnoresOtherEvents(t *testing.T) {
	w, tel, cancel := newTestWorld(t, 1)
	defer cancel()
	p := NewTickWorkerPool(w, tel)

	p.handleEvent(nil, &sim.ActorArrived{ActorID: "alice"})

	if len(p.jobs) != 0 {
		t.Fatal("handleEvent enqueued a job for a non-ReactorTickDue event")
	}
}

func TestHandleEventPanicsWhenBufferFull(t *testing.T) {
	w, tel, cancel := newTestWorld(t, 1)
	defer cancel()
	p := NewTickWorkerPool(w, tel)

	// Fill the buffer (cap 2) so the next enqueue would block. In production
	// this is unreachable — the evaluator's CanAdmit check on the same
	// goroutine guarantees room — so the subscriber panics rather than
	// dropping a job whose warrants were already consumed.
	p.jobs <- tickJob{}
	p.jobs <- tickJob{}

	assertPanics(t, "enqueue against a full buffer", func() {
		p.handleEvent(nil, &sim.ReactorTickDue{ActorID: "alice", AttemptID: "A1"})
	})
}

// TestSubscriberInvariantHoldsAcrossMultiActorScan proves the subscriber's
// panic invariant holds under the REAL evaluator, not just by argument:
// with a buffer of 2 and three actors due in a single EvaluateReactors
// scan, the evaluator's per-actor CanAdmit check observes each prior
// actor's completed enqueue — World.emit dispatches subscribers
// synchronously, inline, before the scan advances to the next actor — so
// exactly two are admitted and the third is deferred, with no panic.
func TestSubscriberInvariantHoldsAcrossMultiActorScan(t *testing.T) {
	ids := []sim.ActorID{"a0", "a1", "a2"}
	w, tel, cancel := newTestWorldWithActors(t, ids, 1) // worker count 1 → buffer cap 2
	defer cancel()
	p := NewTickWorkerPool(w, tel)
	registerPool(t, w, p)

	now := time.Now().UTC()
	for _, id := range ids {
		seedDueWarrantFor(t, w, id, now)
	}
	if _, err := w.Send(sim.EvaluateReactors(now)); err != nil {
		t.Fatalf("EvaluateReactors: %v", err)
	}

	// Buffer cap is 2 — exactly two jobs admitted.
	if got := len(p.jobs); got != 2 {
		t.Fatalf("expected 2 admitted jobs (buffer cap 2), got %d", got)
	}
	// Exactly one actor still has an open warrant cycle — deferred, nothing
	// consumed. (Which one is nondeterministic: Go map iteration order.)
	v, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		deferred := 0
		for _, id := range ids {
			if world.Actors[id].WarrantedSince != nil {
				deferred++
			}
		}
		return deferred, nil
	}})
	if err != nil {
		t.Fatalf("read actors: %v", err)
	}
	if v.(int) != 1 {
		t.Fatalf("expected exactly 1 deferred actor, got %d", v.(int))
	}
	// And a `deferred` telemetry record was written for the un-admitted one.
	deferred := false
	for _, rec := range tel.snapshot() {
		if rec.Kind == "deferred" {
			deferred = true
		}
	}
	if !deferred {
		t.Fatal("expected a `deferred` telemetry record for the un-admitted actor")
	}
}
