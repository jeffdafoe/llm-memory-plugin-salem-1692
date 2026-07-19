package sim_test

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// pc_sleep_restart_test.go — LLM-473. The idle auto-bed across an engine restart,
// driven through the real load path.
//
// These cases live in the external test package because they need a Repository
// (engine/sim/repo/mem imports sim, so the internal package cannot reach it) and
// because the point is to exercise World.FinalizeLoad rather than call the seed
// helper directly — a helper call would still pass if the FinalizeLoad wiring
// were dropped, misplaced, or skipped by an early return on one load path.
//
// Every read of world-owned state goes through w.Send. The world goroutine is
// running from the moment the fixture returns, so touching w.Actors or w.LoadedAt
// directly would be a data race under -race even where the value looks immutable.

// buildRestartWorld loads a world the way a boot does, containing one lodger PC:
// standing in the inn's common area, holding an active ledger grant for private
// bedroom 1, and tired at the observed live cap of 24. Its transient stamps are
// nil exactly as they are for every PC after a restart.
//
// The returned world is RUNNING. Cleanup cancels the run loop and WAITS for it,
// so a wedged world surfaces as a failure in the test that caused it rather than
// as a leaked goroutine in some later one.
func buildRestartWorld(t *testing.T) *sim.World {
	t.Helper()
	repo, handles := mem.NewRepository()
	expires := time.Now().UTC().Add(72 * time.Hour)
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"jefferey": {
			ID:                "jefferey",
			Kind:              sim.KindPC,
			DisplayName:       "Jefferey",
			LoginUsername:     "jeff",
			InsideStructureID: "inn",
			// Literal key, matching the existing sleep fixtures — this codebase has
			// no exported NeedKey constant for tiredness to reference.
			Needs: map[sim.NeedKey]int{"tiredness": 24},
			RoomAccess: map[sim.RoomAccessKey]*sim.RoomAccess{
				{RoomID: 1, Source: sim.AccessSourceLedger}: {
					RoomID: 1, Source: sim.AccessSourceLedger, Active: true, ExpiresAt: &expires,
				},
			},
		},
	})
	handles.Structures.Seed(map[sim.StructureID]*sim.Structure{
		"inn": {ID: "inn", DisplayName: "Inn", Rooms: []*sim.Room{
			{ID: 1, StructureID: "inn", Kind: sim.RoomKindPrivate, Name: "bedroom_1"},
			{ID: 2, StructureID: "inn", Kind: sim.RoomKindCommon, Name: "tavern"},
		}},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("world run loop did not return within 5s of cancel")
		}
	})
	return w
}

// loadedAt reads the world's boot instant on the world goroutine, and asserts it
// is set — the seed derives from it, so a zero value would silently stamp every
// PC's idle clock at the zero time and the bed-down would fire immediately.
func loadedAt(t *testing.T, w *sim.World) time.Time {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return world.LoadedAt, nil
	}})
	if err != nil {
		t.Fatalf("read LoadedAt: %v", err)
	}
	at := res.(time.Time)
	if at.IsZero() {
		t.Fatal("LoadedAt must be set before the restart passes run — the idle-clock seed derives from it")
	}
	return at
}

func inputStamp(t *testing.T, w *sim.World, id sim.ActorID) *time.Time {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return world.Actors[id].LastPCInputAt, nil
	}})
	if err != nil {
		t.Fatalf("read LastPCInputAt: %v", err)
	}
	return res.(*time.Time)
}

func stampPresence(t *testing.T, w *sim.World, id sim.ActorID, at time.Time) {
	t.Helper()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors[id].LastPCSeenAt = &at
		return nil, nil
	}}); err != nil {
		t.Fatalf("stamp presence: %v", err)
	}
}

func sleeping(t *testing.T, w *sim.World, id sim.ActorID) bool {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return world.Actors[id].SleepingUntil != nil, nil
	}})
	if err != nil {
		t.Fatalf("read SleepingUntil: %v", err)
	}
	return res.(bool)
}

// The seed must happen as part of loading a world, not merely be available to be
// called: FinalizeLoad is the single place both supported load paths converge
// (sim.LoadWorld and the pg orchestrator, which calls w.FinalizeLoad directly).
func TestFinalizeLoad_SeedsPCInputStampAtBoot(t *testing.T) {
	w := buildRestartWorld(t)

	stamp := inputStamp(t, w, "jefferey")
	if stamp == nil {
		t.Fatal("loading a world must seed every PC's idle clock — a nil stamp is what the idle auto-bed reads as 'never idle'")
	}
	if boot := loadedAt(t, w); !stamp.Equal(boot) {
		t.Errorf("LastPCInputAt = %v, want the world's boot instant %v (one shared restart 'now')", stamp, boot)
	}
}

// TestAutoBedIdle_ConnectedLodgerBedsDownAfterRestart is the LLM-473 regression:
// the live shape both ZBBS-WORK-324 and LLM-450 walked past.
//
// A player lodged at the tavern with the browser tab still open. The WS heartbeat
// keeps LastPCSeenAt fresh, so the offline arm correctly declines to bed them —
// which is precisely why the idle arm has to work. Before this fix LastPCInputAt
// was nil after the restart, the idle arm skipped them too, and NOTHING bedded
// them for the rest of the session while tiredness climbed to the cap.
//
// Both sweeps run here, in the order runPCSleepTick runs them, so the test fails
// if the fix is ever "achieved" by making the offline arm swallow a connected
// player instead.
func TestAutoBedIdle_ConnectedLodgerBedsDownAfterRestart(t *testing.T) {
	w := buildRestartWorld(t)
	sweepAt := loadedAt(t, w).Add((sim.DefaultPCIdleSleepMinutes + 1) * time.Minute)

	// The tab is open: the heartbeat stamped presence moments ago. Fresh relative
	// to the SWEEP instant, not to boot — an aged stamp would silently put this
	// PC on the disconnected branch and the test would prove nothing.
	stampPresence(t, w, "jefferey", sweepAt.Add(-5*time.Second))

	if _, err := w.Send(sim.AutoBedOfflineLodgerPCs(sweepAt)); err != nil {
		t.Fatalf("AutoBedOfflineLodgerPCs: %v", err)
	}
	if sleeping(t, w, "jefferey") {
		t.Fatal("the offline arm must skip a PC whose client is still attached — that is why the idle arm must cover this case")
	}

	if _, err := w.Send(sim.AutoBedIdleLodgerPCs(sweepAt)); err != nil {
		t.Fatalf("AutoBedIdleLodgerPCs: %v", err)
	}
	if !sleeping(t, w, "jefferey") {
		t.Fatal("a connected, idle, tired lodger must bed down after a restart, not stay awake to the tiredness cap")
	}
}

// Inside the post-boot grace period the player keeps the floor: a reconnecting
// client must not have control taken away on the first sweep after a deploy.
func TestAutoBedIdle_ConnectedLodgerKeepsFloorInsideGracePeriod(t *testing.T) {
	w := buildRestartWorld(t)
	sweepAt := loadedAt(t, w).Add((sim.DefaultPCIdleSleepMinutes - 1) * time.Minute)

	stampPresence(t, w, "jefferey", sweepAt.Add(-5*time.Second))

	if _, err := w.Send(sim.AutoBedIdleLodgerPCs(sweepAt)); err != nil {
		t.Fatalf("AutoBedIdleLodgerPCs: %v", err)
	}
	if sleeping(t, w, "jefferey") {
		t.Error("a PC inside the post-boot grace period must not be bedded yet")
	}
}
