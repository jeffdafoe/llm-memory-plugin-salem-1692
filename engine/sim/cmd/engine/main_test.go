package main

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/llm"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// main_test.go — lifecycle smoke test for the engine entrypoint. It exercises
// the part that build-checking can't: that run() wires the full runtime, boots
// the world, drives the periodic checkpointer, and on shutdown stops the
// periodic loop, takes a final checkpoint while the world goroutine is still
// alive, and returns cleanly. The shutdown ORDERING is the subtle bit — a
// final checkpoint after the world goroutine stopped would deadlock, and a
// periodic write overlapping the final one would race.
//
// Mem-backed (sim.LoadWorld) with a fake LLM client + a capturing save, so no
// pg or network is involved. A quiet empty world fires no ticks/agent cascades;
// the atmosphere cascade does fire one immediate off-world sweep on boot, which
// calls the fake client and gets a harmless script-exhausted error (logged +
// ignored per atmosphere's failure semantics) — it doesn't touch the checkpoint
// lifecycle this test asserts.

// TestRun_LifecycleAndFinalCheckpoint boots run() against a mem world with a
// fast checkpoint cadence, lets it tick, signals shutdown, and asserts a
// checkpoint was captured (the periodic loop AND/OR the final write) and that
// run() returned.
func TestRun_LifecycleAndFinalCheckpoint(t *testing.T) {
	repo, _ := mem.NewRepository()
	world, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	// Fast cadence so the periodic checkpointer fires within the test window.
	world.Settings.CheckpointInterval = 20 * time.Millisecond

	var mu sync.Mutex
	var saves int
	var last *sim.CheckpointSnapshot
	save := func(_ context.Context, cp *sim.CheckpointSnapshot) error {
		mu.Lock()
		defer mu.Unlock()
		saves++
		last = cp
		return nil
	}

	rt := runtime{
		World:     world,
		LLMClient: llm.NewFakeClient(), // atmosphere's boot sweep calls this once → harmless script-exhausted error
		Save:      save,
		TickSink:  nil, // worker pool null-checks the sink
	}

	stop := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- run(rt, stop) }()

	// Let the world boot and the periodic checkpointer fire at least once.
	time.Sleep(150 * time.Millisecond)

	mu.Lock()
	periodicSaves := saves
	mu.Unlock()
	if periodicSaves == 0 {
		t.Error("expected at least one periodic checkpoint before shutdown")
	}

	// Signal shutdown and wait for run() to return.
	close(stop)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not return within 5s of shutdown signal")
	}

	// run() must not return until the world goroutine has actually stopped
	// (so the caller can safely tear down the pool). A command sent after
	// return should fail rather than be serviced.
	sctx, scancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer scancel()
	if _, err := world.SendContext(sctx, sim.Command{Fn: func(*sim.World) (any, error) { return nil, nil }}); err == nil {
		t.Error("world still processing commands after run() returned — run did not wait for world stop")
	}

	// The final checkpoint must have run during shutdown.
	mu.Lock()
	defer mu.Unlock()
	if last == nil {
		t.Fatal("no checkpoint snapshot was ever captured")
	}
	if saves <= periodicSaves {
		t.Errorf("expected a final checkpoint after shutdown (saves=%d, periodic=%d)", saves, periodicSaves)
	}
}

// TestRun_WiresOffWorldCascades proves run() reaches the off-world LLM cascade
// set (RegisterProductionCascades wires atmosphere / consolidation / narrative
// consolidation / noticeboard + the ActionLog substrate) into the live runtime
// — the seam build-checking can't catch. Atmosphere is the witness: its
// immediate first sweep calls the LLM unconditionally (world-level, not
// candidate-gated like the consolidations, which make no call on an empty
// world). So if RegisterProductionCascades weren't reached, Environment.
// Atmosphere would stay empty. We script one atmosphere line and assert it gets
// installed after boot.
func TestRun_WiresOffWorldCascades(t *testing.T) {
	repo, _ := mem.NewRepository()
	world, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	world.Settings.CheckpointInterval = 20 * time.Millisecond

	const wantAtmosphere = "The village lies still beneath a watchful sky."
	rt := runtime{
		World:     world,
		LLMClient: llm.NewFakeClient(llm.ScriptedTurn{Response: llm.Response{Content: wantAtmosphere}}),
		Save:      func(context.Context, *sim.CheckpointSnapshot) error { return nil },
		TickSink:  nil,
	}

	stop := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- run(rt, stop) }()

	// The immediate atmosphere sweep applies async (via SendContext) once Run
	// starts, so poll the world for the installed prose rather than racing it.
	var got string
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		res, sendErr := world.SendContext(context.Background(), sim.Command{Fn: func(w *sim.World) (any, error) {
			return w.Environment.Atmosphere, nil
		}})
		if sendErr == nil {
			if s, _ := res.(string); s != "" {
				got = s
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}

	close(stop)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not return within 5s of shutdown signal")
	}

	if got != wantAtmosphere {
		t.Errorf("Environment.Atmosphere = %q, want %q (RegisterAtmosphere not wired into run()?)", got, wantAtmosphere)
	}
}
