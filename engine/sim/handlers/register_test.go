package handlers

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// register_test.go — RegisterTickHandlers wires BOTH halves of the bootstrap
// unit: the admission controller and the ReactorTickDue subscriber. Each
// test below proves one half is actually connected, plus nil-arg rejection.

// registerPool installs pool into the running world via RegisterTickHandlers
// (which must run on the world goroutine once Run has started).
func registerPool(t *testing.T, w *sim.World, pool *TickWorkerPool) {
	t.Helper()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		RegisterTickHandlers(world, pool)
		return nil, nil
	}}); err != nil {
		t.Fatalf("RegisterTickHandlers: %v", err)
	}
}

// TestRegisterTickHandlersWiresSubscriber: a due actor the evaluator admits
// produces a job on the pool's channel — proving Subscribe was wired.
func TestRegisterTickHandlersWiresSubscriber(t *testing.T) {
	w, tel, cancel := newTestWorld(t, 1)
	defer cancel()
	p := NewTickWorkerPool(w, tel)
	registerPool(t, w, p)

	now := time.Now().UTC()
	seedDueWarrant(t, w, now)
	if _, err := w.Send(sim.EvaluateReactors(now)); err != nil {
		t.Fatalf("EvaluateReactors: %v", err)
	}

	select {
	case job := <-p.jobs:
		if job.actorID != "alice" {
			t.Fatalf("enqueued job is for the wrong actor: %+v", job)
		}
	default:
		t.Fatal("evaluator admitted the tick but no job reached the pool — subscriber not wired")
	}
}

// TestRegisterTickHandlersWiresAdmissionGate: with the pool's buffer full,
// the evaluator defers — warrants stay open, nothing is consumed, and a
// `deferred` telemetry record is written — proving the pool was installed
// as the admission controller.
func TestRegisterTickHandlersWiresAdmissionGate(t *testing.T) {
	w, tel, cancel := newTestWorld(t, 1)
	defer cancel()
	p := NewTickWorkerPool(w, tel)
	registerPool(t, w, p)

	// Fill the buffer (cap 2) so CanAdmit() returns false.
	p.jobs <- tickJob{}
	p.jobs <- tickJob{}

	now := time.Now().UTC()
	seedDueWarrant(t, w, now)
	if _, err := w.Send(sim.EvaluateReactors(now)); err != nil {
		t.Fatalf("EvaluateReactors: %v", err)
	}

	v, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a := world.Actors["alice"]
		return a.WarrantedSince != nil && !a.TickInFlight, nil
	}})
	if err != nil {
		t.Fatalf("read actor: %v", err)
	}
	if !v.(bool) {
		t.Fatal("admission gate not wired — warrants were consumed despite a full pool")
	}

	deferred := false
	for _, rec := range tel.snapshot() {
		if rec.Kind == "deferred" && rec.ActorID == "alice" {
			deferred = true
		}
	}
	if !deferred {
		t.Fatal("no `deferred` telemetry record — admission gate not wired")
	}
}

func TestRegisterTickHandlersRejectsNil(t *testing.T) {
	w, tel, cancel := newTestWorld(t, 1)
	defer cancel()
	p := NewTickWorkerPool(w, tel)

	assertPanics(t, "a nil world", func() { RegisterTickHandlers(nil, p) })
	assertPanics(t, "a nil pool", func() { RegisterTickHandlers(w, nil) })
}

// TestRegisterTickHandlersRejectsMismatchedWorld: a pool built for world A
// must not be wired into world B — that would split the pipeline (admission
// + enqueue on B, completion on A).
func TestRegisterTickHandlersRejectsMismatchedWorld(t *testing.T) {
	w1, tel, cancel1 := newTestWorld(t, 1)
	defer cancel1()
	w2, _, cancel2 := newTestWorld(t, 1)
	defer cancel2()

	p := NewTickWorkerPool(w1, tel) // built for w1
	assertPanics(t, "a pool built for a different world", func() {
		RegisterTickHandlers(w2, p)
	})
}
