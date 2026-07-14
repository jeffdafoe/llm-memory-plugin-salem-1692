package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/llm"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// stop_test.go — the LLM-404 stop gate: a graceful stop must not exit onto a
// stale checkpoint, and a force stop must exit regardless.
//
// All three tests drive run() with a checkpoint writer that FAILS, which is the
// state the 2026-07-12 outage was actually in for 17.5 hours. The distinction
// the ticket turns on is what the engine does about it at exit time.

// errCheckpointBroken stands in for the real thing — a poisoned row, a dead
// pool, a constraint the world can no longer satisfy.
var errCheckpointBroken = errors.New("save world: relation \"actor\" violates check constraint")

// flakySave is a CheckpointFunc whose success can be flipped mid-test, so a test
// can break durability, watch the graceful stop refuse, then repair durability
// and watch the same request now succeed — the exact operator sequence LLM-404
// is built around.
type flakySave struct {
	mu     sync.Mutex
	fail   bool
	writes int
}

func (f *flakySave) save(context.Context, *sim.CheckpointSnapshot) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes++
	if f.fail {
		return errCheckpointBroken
	}
	return nil
}

func (f *flakySave) setFail(fail bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail = fail
}

func (f *flakySave) writeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.writes
}

// stopHarness is a booted engine plus the two levers a test pulls on it.
type stopHarness struct {
	world    *sim.World
	saver    *flakySave
	force    chan struct{}
	graceful chan struct{}
	done     chan error
}

// bootStopTestWorld builds a mem world with the periodic checkpointer pushed out
// of the way (a one-hour cadence never fires inside a test), so every durable
// write these tests observe is one the STOP path made — not a periodic one that
// happened to land mid-assertion.
func bootStopTestWorld(t *testing.T) *stopHarness {
	t.Helper()
	repo, _ := mem.NewRepository()
	world, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	world.Settings.CheckpointInterval = time.Hour

	saver := &flakySave{}
	rt := runtime{
		World:     world,
		LLMClient: llm.NewFakeClient(), // atmosphere's boot sweep calls this once — harmless
		Save:      saver.save,
		TickSink:  nil,
	}

	h := &stopHarness{
		world:    world,
		saver:    saver,
		force:    make(chan struct{}, 1),
		graceful: make(chan struct{}, 1),
		done:     make(chan error, 1),
	}
	go func() {
		h.done <- run(rt, stopSignals{force: h.force, graceful: h.graceful})
	}()
	return h
}

// requireStopped asserts run() returned cleanly within the window.
func (h *stopHarness) requireStopped(t *testing.T, why string) {
	t.Helper()
	select {
	case err := <-h.done:
		if err != nil {
			t.Fatalf("run returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("run did not return within 5s: %s", why)
	}
}

// worldIsAlive reports whether the world goroutine is still servicing commands —
// i.e. whether the village is still running. This is the assertion that an
// aborted stop left the engine INTACT rather than half-torn-down.
func worldIsAlive(w *sim.World) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	_, err := w.SendContext(ctx, sim.Command{Fn: func(*sim.World) (any, error) { return nil, nil }})
	return err == nil
}

// TestRun_GracefulStopAbortsOnFailedCheckpointThenCompletesOnRepair is the
// ticket in one test. Durability is broken; a graceful stop must REFUSE to exit
// and leave the village running. The operator then repairs durability and asks
// again — and now the stop completes, with the world saved and nothing lost.
func TestRun_GracefulStopAbortsOnFailedCheckpointThenCompletesOnRepair(t *testing.T) {
	h := bootStopTestWorld(t)
	h.saver.setFail(true)

	h.graceful <- struct{}{}

	// The gate must refuse. run() keeps operating instead of returning.
	select {
	case err := <-h.done:
		t.Fatalf("run() EXITED on a graceful stop with a failing checkpoint (err=%v) — "+
			"this is the 17.5-hour rollback the gate exists to prevent", err)
	case <-time.After(time.Second):
	}

	// ...and the refusal must leave the engine whole, not half-torn-down. If the
	// gate had run after the teardown (the design LLM-404 rejected), the world
	// goroutine would be gone and there would be nothing left to resume.
	if !worldIsAlive(h.world) {
		t.Fatal("world goroutine stopped after an ABORTED graceful stop — the abort tore down the engine it was supposed to preserve")
	}
	if h.saver.writeCount() == 0 {
		t.Error("graceful stop attempted no checkpoint at all — the gate never ran")
	}

	// The operator repairs durability. The next graceful stop now finds a world it
	// can save, and goes through.
	h.saver.setFail(false)
	h.graceful <- struct{}{}
	h.requireStopped(t, "a graceful stop whose checkpoint SUCCEEDS must complete")

	if worldIsAlive(h.world) {
		t.Error("world still processing commands after run() returned — the completed stop did not tear the world down")
	}
}

// TestRun_ForceStopExitsDespiteFailingCheckpoint — the other half of the
// contract. Force is "I accept the loss": the process must come down even though
// the world cannot be saved, because the alternative (an engine that can never be
// stopped) is worse than the rollback.
func TestRun_ForceStopExitsDespiteFailingCheckpoint(t *testing.T) {
	h := bootStopTestWorld(t)
	h.saver.setFail(true)

	h.force <- struct{}{}
	h.requireStopped(t, "force must exit regardless of checkpoint state")

	if worldIsAlive(h.world) {
		t.Error("world still processing commands after a force stop returned")
	}
}

// TestRun_ForceStopPreemptsAbortedGraceful — the escalation path. Durability is
// broken, the operator asks nicely, the engine refuses. The operator then decides
// the loss is acceptable and forces it. That force must be honoured immediately,
// even though a graceful request is sitting right there in the channel: "exits
// regardless" is worthless if force can be made to queue behind another
// multi-second gate checkpoint first.
func TestRun_ForceStopPreemptsAbortedGraceful(t *testing.T) {
	h := bootStopTestWorld(t)
	h.saver.setFail(true)

	// Refuse a graceful stop first, so run() is back at the gate and the engine is
	// in exactly the state an escalating operator would find it in.
	h.graceful <- struct{}{}
	select {
	case <-h.done:
		t.Fatal("run() exited on a graceful stop with a failing checkpoint")
	case <-time.After(time.Second):
	}

	// Queue ANOTHER graceful (the operator retrying) and a force behind it. Force
	// must win, and must not wait out a second gate checkpoint to do it.
	h.graceful <- struct{}{}
	h.force <- struct{}{}

	h.requireStopped(t, "force must preempt a pending graceful request, not queue behind it")
}

// TestRun_GracefulStopCheckpointsBeforeTeardown pins the ordering that makes the
// abort recoverable: the gate checkpoint runs while the engine is still fully
// live, BEFORE the teardown, and the final checkpoint still runs after the tick
// pool drains. Two durable writes on a clean graceful stop, with no periodic
// checkpointer in play to account for either of them.
func TestRun_GracefulStopCheckpointsBeforeTeardown(t *testing.T) {
	h := bootStopTestWorld(t)

	// Nothing should have been written yet — the periodic cadence is an hour out,
	// and a freshly-loaded world is already identical to what is on disk.
	if got := h.saver.writeCount(); got != 0 {
		t.Fatalf("checkpoint writes before any stop request = %d, want 0", got)
	}
	if !worldIsAlive(h.world) {
		t.Fatal("world goroutine never started")
	}

	h.graceful <- struct{}{}
	h.requireStopped(t, "a clean graceful stop must complete")

	// One for the gate (engine live), one for the final write (pool drained). The
	// second is not redundant — the tick pool's workers commit their in-flight
	// ticks as they drain, AFTER the gate write, and the final checkpoint is what
	// captures them. Both are safe to run back-to-back: SaveWorld upserts the
	// snapshot at a fresh snapshot_gen and sweeps below it, so what it persists is
	// a pure function of the snapshot it was handed.
	if got := h.saver.writeCount(); got != 2 {
		t.Errorf("checkpoint writes over a graceful stop = %d, want 2 (the pre-teardown gate + the final checkpoint)", got)
	}
}
