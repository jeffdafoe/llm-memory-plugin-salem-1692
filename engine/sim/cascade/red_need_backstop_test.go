package cascade_test

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/cascade"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// red_need_backstop_test.go — driver-side tests for the red-need backstop
// cascade slice (ZBBS-HOME-363). The substrate Command
// (sim.EvaluateRedNeedBackstop) + its exponential backoff have their own
// test surface in engine/sim/red_need_backstop_commands_test.go; these cover
// the goroutine lifecycle, the immediate-first-sweep guarantee, the ticker
// cadence, and the ctx-cancel exit path.

// buildRedNeedDriverWorld stands up a world with one starving NPC (hunger 24,
// red) and tight settings: a tiny base delay so a re-stamp lands promptly
// after a clear, and a fast sweep interval.
func buildRedNeedDriverWorld(t *testing.T) (*sim.World, context.CancelFunc) {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"hannah": {ID: "hannah", Kind: sim.KindNPCShared, Needs: map[sim.NeedKey]int{"hunger": 24}},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go w.Run(ctx)

	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Settings.RedNeedBackstopBaseDelay = time.Nanosecond
		world.Settings.RedNeedBackstopSweepInterval = 20 * time.Millisecond
		return nil, nil
	}}); err != nil {
		cancel()
		t.Fatalf("seed settings: %v", err)
	}
	return w, cancel
}

func redNeedWarranted(t *testing.T, w *sim.World, id sim.ActorID) bool {
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
		t.Fatalf("redNeedWarranted: %v", err)
	}
	return warranted
}

func clearRedNeedWarrant(t *testing.T, w *sim.World, id sim.ActorID) {
	t.Helper()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a := world.Actors[id]
		a.WarrantedSince = nil
		a.WarrantDueAt = nil
		a.Warrants = nil
		return nil, nil
	}}); err != nil {
		t.Fatalf("clear warrant: %v", err)
	}
}

// TestRegisterRedNeedBackstop_StampsViaSweep: the driver fires the immediate
// first sweep and warrants the starving actor.
func TestRegisterRedNeedBackstop_StampsViaSweep(t *testing.T) {
	w, cancel := buildRedNeedDriverWorld(t)
	defer cancel()

	driverCtx, driverCancel := context.WithCancel(context.Background())
	defer driverCancel()
	cascade.RegisterRedNeedBackstop(driverCtx, w)

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if redNeedWarranted(t, w, "hannah") {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("red-need backstop did not warrant hannah within 500ms")
}

// TestRegisterRedNeedBackstop_TickerFiresRepeatedly: after the first warrant
// is cleared, the ticker fires again and re-stamps (goroutine persists).
func TestRegisterRedNeedBackstop_TickerFiresRepeatedly(t *testing.T) {
	w, cancel := buildRedNeedDriverWorld(t)
	defer cancel()

	driverCtx, driverCancel := context.WithCancel(context.Background())
	defer driverCancel()
	cascade.RegisterRedNeedBackstop(driverCtx, w)

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) && !redNeedWarranted(t, w, "hannah") {
		time.Sleep(5 * time.Millisecond)
	}
	if !redNeedWarranted(t, w, "hannah") {
		t.Fatal("first sweep did not warrant hannah")
	}

	clearRedNeedWarrant(t, w, "hannah")
	if redNeedWarranted(t, w, "hannah") {
		t.Fatal("warrant was not cleared")
	}

	deadline = time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if redNeedWarranted(t, w, "hannah") {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("ticker did not re-stamp hannah within 500ms after clear")
}

// TestRegisterRedNeedBackstop_CtxCancelExitsGoroutine: cancelling the ctx
// stops the sweep — no re-stamp after a post-cancel clear.
func TestRegisterRedNeedBackstop_CtxCancelExitsGoroutine(t *testing.T) {
	w, cancel := buildRedNeedDriverWorld(t)
	defer cancel()

	driverCtx, driverCancel := context.WithCancel(context.Background())
	cascade.RegisterRedNeedBackstop(driverCtx, w)

	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) && !redNeedWarranted(t, w, "hannah") {
		time.Sleep(5 * time.Millisecond)
	}
	if !redNeedWarranted(t, w, "hannah") {
		t.Fatal("first sweep did not warrant hannah")
	}

	driverCancel()
	time.Sleep(100 * time.Millisecond) // drain any in-flight sweep
	clearRedNeedWarrant(t, w, "hannah")

	time.Sleep(150 * time.Millisecond) // > one sweep interval
	if redNeedWarranted(t, w, "hannah") {
		t.Error("red-need backstop re-stamped after ctx cancel; goroutine didn't exit")
	}
}

// TestRegisterRedNeedBackstop_PanicsOnNilWorld pins the wiring-time guard.
func TestRegisterRedNeedBackstop_PanicsOnNilWorld(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("RegisterRedNeedBackstop(nil world) did not panic")
		}
	}()
	cascade.RegisterRedNeedBackstop(context.Background(), nil)
}
