package cascade_test

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/cascade"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// visitor_test.go — driver-side tests for the visitor cascade slice.
// The substrate Command (sim.TickVisitorCascade) is exercised at depth in
// engine/sim/visitor_test.go; these tests cover the goroutine lifecycle,
// the immediate-first-tick guarantee, and the ctx-cancel exit path.

// allDirtTerrain returns a Terrain blob of MapW*MapH dirt tiles —
// duplicates the helper in the sim package's visitor_test.go so this
// _test file is self-contained.
func allDirtTerrain() *sim.Terrain {
	data := make([]byte, sim.MapW*sim.MapH)
	for i := range data {
		data[i] = sim.TerrainDirt
	}
	return &sim.Terrain{Data: data}
}

// buildVisitorDriverWorld stands up a world with terrain seeded, a
// pre-expired visitor in place, and the cascade tick interval tuned
// for fast firing. The expired visitor + grace-past ExpiresAt is the
// observable signal the driver pumps cleanup.
func buildVisitorDriverWorld(t *testing.T) (*sim.World, context.CancelFunc) {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.Terrain.Seed(allDirtTerrain())
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go w.Run(ctx)

	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Settings.VisitorTickInterval = 20 * time.Millisecond
		// Spawn disabled so the driver only exercises cleanup.
		world.Settings.VisitorSpawnChancePermille = 0
		world.Actors["expired"] = &sim.Actor{
			ID:          "expired",
			DisplayName: "Elias Drum the peddler",
			Kind:        sim.KindNPCShared,
			LLMAgent:    sim.VisitorAgentName,
			VisitorState: &sim.VisitorState{
				Archetype: "peddler",
				ExpiresAt: time.Now().Add(-time.Duration(sim.VisitorCleanupGraceMinutes+1) * time.Minute),
			},
		}
		return nil, nil
	}}); err != nil {
		cancel()
		t.Fatalf("seed: %v", err)
	}
	return w, cancel
}

func actorPresent(t *testing.T, w *sim.World, id sim.ActorID) bool {
	t.Helper()
	var present bool
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		_, present = world.Actors[id]
		return nil, nil
	}}); err != nil {
		t.Fatalf("actorPresent: %v", err)
	}
	return present
}

// TestRegisterVisitor_NilWorldPanics verifies the fail-fast wiring guard.
func TestRegisterVisitor_NilWorldPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("RegisterVisitor(nil) did not panic")
		}
	}()
	cascade.RegisterVisitor(context.Background(), nil)
}

// TestRegisterVisitor_ImmediateFirstTick verifies the driver fires an
// initial tick on registration (no initial-interval wait) and the
// expired visitor is cleaned up via that first tick.
func TestRegisterVisitor_ImmediateFirstTick(t *testing.T) {
	w, cancel := buildVisitorDriverWorld(t)
	defer cancel()

	driverCtx, driverCancel := context.WithCancel(context.Background())
	defer driverCancel()
	cascade.RegisterVisitor(driverCtx, w)

	// The immediate first tick should drive cleanup against the expired
	// visitor. Poll briefly to give SendContext + Run round-trip time.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if !actorPresent(t, w, "expired") {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("driver did not clean up expired visitor within 500ms")
}

// TestRegisterVisitor_CtxCancelExits verifies the driver goroutine
// returns promptly on ctx cancel. Detected indirectly: after cancel the
// driver no longer holds the world goroutine sending Commands, so a
// follow-up command-channel send completes immediately.
func TestRegisterVisitor_CtxCancelExits(t *testing.T) {
	w, cancel := buildVisitorDriverWorld(t)
	defer cancel()

	driverCtx, driverCancel := context.WithCancel(context.Background())
	cascade.RegisterVisitor(driverCtx, w)

	// Wait for the first cleanup so we know the driver is running.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) && actorPresent(t, w, "expired") {
		time.Sleep(5 * time.Millisecond)
	}
	driverCancel()

	// World should still be responsive after driver cancel.
	done := make(chan struct{})
	go func() {
		_, _ = w.Send(sim.Command{Fn: func(world *sim.World) (any, error) { return nil, nil }})
		close(done)
	}()
	select {
	case <-done:
		// good
	case <-time.After(500 * time.Millisecond):
		t.Fatal("world unresponsive 500ms after driver cancel")
	}
}
