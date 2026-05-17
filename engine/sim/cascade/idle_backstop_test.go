package cascade_test

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/cascade"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// idle_backstop_test.go — driver-side tests for the idle-backstop
// cascade slice. The substrate Command (sim.EvaluateIdleBackstop)
// has its own test surface in engine/sim/idle_backstop_commands_test.go;
// these tests cover the goroutine lifecycle, the immediate-first-sweep
// guarantee, the ticker cadence, and the ctx-cancel exit path.

// buildBackstopDriverWorld stands up a tiny world with one stale
// shared NPC, settings tuned so the sweep cadence is fast enough for
// tests, and runs it.
func buildBackstopDriverWorld(t *testing.T) (*sim.World, context.CancelFunc) {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"hannah": {ID: "hannah", Kind: sim.KindNPCShared},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go w.Run(ctx)

	// Tight settings:
	// - Threshold 1 ns so the actor is "past threshold" the moment the
	//   sweep computes `now` (any non-zero elapsed time qualifies).
	// - SweepInterval 20 ms so the ticker fires fast in tests.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Settings.IdleBackstopThreshold = time.Nanosecond
		world.Settings.IdleBackstopSweepInterval = 20 * time.Millisecond
		return nil, nil
	}}); err != nil {
		cancel()
		t.Fatalf("seed settings: %v", err)
	}
	return w, cancel
}

// readActorWarranted polls actor state through a Send Command; returns
// whether the actor currently has an open warrant cycle.
func readActorWarranted(t *testing.T, w *sim.World, id sim.ActorID) bool {
	t.Helper()
	var warranted bool
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a, ok := world.Actors[id]
		if !ok {
			t.Fatalf("actor %q not found", id)
		}
		warranted = a.WarrantedSince != nil
		return nil, nil
	}}); err != nil {
		t.Fatalf("readActorWarranted: %v", err)
	}
	return warranted
}

// TestRegisterIdleBackstop_StampsViaSweep verifies that the driver
// goroutine fires the immediate first sweep and stamps a warrant on a
// qualifying actor, well before the ticker would have fired.
func TestRegisterIdleBackstop_StampsViaSweep(t *testing.T) {
	w, cancel := buildBackstopDriverWorld(t)
	defer cancel()

	driverCtx, driverCancel := context.WithCancel(context.Background())
	defer driverCancel()
	cascade.RegisterIdleBackstop(driverCtx, w)

	// First sweep is immediate — wait briefly for the SendContext round-
	// trip to complete and the warrant to land on the actor.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if readActorWarranted(t, w, "hannah") {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("idle backstop did not warrant hannah within 500ms")
}

// TestRegisterIdleBackstop_TickerFiresRepeatedly: after the first
// sweep is consumed (warrant cleared), the ticker fires again on the
// next interval and re-stamps. Verifies the goroutine doesn't exit
// after the first sweep.
func TestRegisterIdleBackstop_TickerFiresRepeatedly(t *testing.T) {
	w, cancel := buildBackstopDriverWorld(t)
	defer cancel()

	driverCtx, driverCancel := context.WithCancel(context.Background())
	defer driverCancel()
	cascade.RegisterIdleBackstop(driverCtx, w)

	// Wait for first warrant.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) && !readActorWarranted(t, w, "hannah") {
		time.Sleep(5 * time.Millisecond)
	}
	if !readActorWarranted(t, w, "hannah") {
		t.Fatal("first sweep did not warrant hannah")
	}

	// Clear the actor's warrant manually so the next sweep has fresh
	// criterion B input.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a := world.Actors["hannah"]
		a.WarrantedSince = nil
		a.WarrantDueAt = nil
		a.Warrants = nil
		return nil, nil
	}}); err != nil {
		t.Fatalf("clear warrant: %v", err)
	}
	if readActorWarranted(t, w, "hannah") {
		t.Fatal("warrant was not cleared")
	}

	// Wait for the ticker to fire again. SweepInterval=20ms; allow
	// some slack.
	deadline = time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if readActorWarranted(t, w, "hannah") {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("ticker did not re-stamp hannah within 500ms after clear")
}

// TestRegisterIdleBackstop_CtxCancelExitsGoroutine: cancelling the
// passed ctx unblocks the sweep goroutine. Verifies via the absence of
// new stamps after cancel.
func TestRegisterIdleBackstop_CtxCancelExitsGoroutine(t *testing.T) {
	w, cancel := buildBackstopDriverWorld(t)
	defer cancel()

	driverCtx, driverCancel := context.WithCancel(context.Background())
	cascade.RegisterIdleBackstop(driverCtx, w)

	// Wait for first stamp.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) && !readActorWarranted(t, w, "hannah") {
		time.Sleep(5 * time.Millisecond)
	}
	if !readActorWarranted(t, w, "hannah") {
		t.Fatal("first sweep did not warrant hannah")
	}

	// Cancel driver, then clear warrant. If the goroutine had not
	// exited, the next ticker tick (20ms later) would re-stamp.
	driverCancel()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a := world.Actors["hannah"]
		a.WarrantedSince = nil
		a.WarrantDueAt = nil
		a.Warrants = nil
		return nil, nil
	}}); err != nil {
		t.Fatalf("clear warrant: %v", err)
	}

	// Wait > one sweep interval. No re-stamp should occur.
	time.Sleep(80 * time.Millisecond)
	if readActorWarranted(t, w, "hannah") {
		t.Error("idle backstop re-stamped after ctx cancel; goroutine didn't exit")
	}
}

// TestRegisterIdleBackstop_PanicsOnNilWorld pins the wiring-time
// guard. Fail-fast at registration over silent no-op at runtime.
func TestRegisterIdleBackstop_PanicsOnNilWorld(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("RegisterIdleBackstop(nil world) did not panic")
		}
	}()
	cascade.RegisterIdleBackstop(context.Background(), nil)
}
