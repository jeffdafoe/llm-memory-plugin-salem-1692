package sim_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// TestScheduledFlipReturnsAfterShutdown verifies the round-2 fix on
// scheduleFlips: invoking the timer-callback body AFTER the world has
// shut down must return promptly via LifecycleContext().Err(), not
// deadlock on a cmds-channel send to a dead world goroutine.
//
// This test calls FireScheduledFlip (the unexported callback body
// exposed via export_test.go) directly, so the assertion doesn't depend
// on the random TransitionSpreadSeconds delay actually elapsing inside
// the test window. With the old context.Background() implementation,
// the SendContext would park forever and this test would time out.
func TestScheduledFlipReturnsAfterShutdown(t *testing.T) {
	repo, handles := mem.NewRepository()
	handles.Assets.Seed(map[sim.AssetID]*sim.Asset{
		"lamp": {
			ID:           "lamp",
			DefaultState: "unlit",
			States: []sim.AssetState{
				{ID: 1, State: "unlit", Tags: []string{sim.TagDayActive}},
				{ID: 2, State: "lit", Tags: []string{sim.TagNightActive}},
			},
		},
	})
	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"lamp-1": {ID: "lamp-1", AssetID: "lamp", CurrentState: "unlit"},
	})

	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	worldDone := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(worldDone)
	}()

	// Bring the world up far enough to confirm LifecycleContext is
	// stamped, then tear it down.
	if _, err := w.Send(sim.Command{Fn: func(_ *sim.World) (any, error) { return nil, nil }}); err != nil {
		t.Fatalf("warmup send: %v", err)
	}
	cancel()

	select {
	case <-worldDone:
	case <-time.After(1 * time.Second):
		t.Fatal("world goroutine did not exit on cancel")
	}

	// Now exercise the callback path that scheduleFlips normally arms
	// via time.AfterFunc. With the round-2 fix, FireScheduledFlip pulls
	// w.LifecycleContext() (the cancelled ctx, retained on world exit
	// per the runCtx field comment) and SendContext returns ctx.Err()
	// immediately. Without the fix (context.Background() + cleared
	// runCtx), the SendContext would park on the cmds channel forever.
	done := make(chan struct{})
	go func() {
		sim.FireScheduledFlip(w, sim.PendingFlip{
			ObjectID: "lamp-1",
			NewState: "lit",
			Gen:      w.WorldEventGen.Load(),
		})
		close(done)
	}()

	select {
	case <-done:
		// returned within budget — fix is working
	case <-time.After(2 * time.Second):
		t.Fatal("FireScheduledFlip blocked after shutdown — context plumbing is wrong")
	}
}

// TestTickersShutdownOnContextCancel verifies the SendContext migration
// for every ticker: each tick path bails out within the cancel signal
// instead of blocking on a Send to a dead world goroutine.
//
// Without item-2 SendContext, a ticker firing AFTER the world goroutine
// has exited would deadlock on the unbuffered reply receive and the test
// would time out.
func TestTickersShutdownOnContextCancel(t *testing.T) {
	repo, _ := mem.NewRepository()
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	var wg sync.WaitGroup
	start := func(fn func(context.Context, *sim.World)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			fn(ctx, w)
		}()
	}

	// World goroutine.
	wg.Add(1)
	go func() {
		defer wg.Done()
		w.Run(ctx)
	}()

	// Every ticker.
	start(sim.RunPhaseTicker)
	start(sim.RunNeedsTicker)
	start(sim.RunObjectRefreshRegen)
	start(sim.RunDwellTicker)
	start(sim.RunProduceTicker)
	start(sim.RunRoomSweep)

	// Cancel immediately. Real shutdown unblocks both the ticker's select
	// AND any in-flight SendContext call.
	cancel()

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// all clean
	case <-time.After(3 * time.Second):
		t.Fatal("goroutines did not exit within 3s of ctx cancel — likely a Send-without-context deadlock")
	}
}
