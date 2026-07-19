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

// buildRestartWorld loads a world the way a boot does, containing one lodger PC:
// standing in the inn's common area, holding an active ledger grant for private
// bedroom 1, and tired at the observed live cap of 24. Its transient stamps are
// nil exactly as they are for every PC after a restart.
// The returned world is RUNNING: every assertion here goes through w.Send, which
// hands the closure to the world goroutine and blocks until it executes.
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
			Needs:             map[sim.NeedKey]int{"tiredness": 24},
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
	t.Cleanup(cancel)
	go w.Run(ctx)
	return w
}

// The seed must happen as part of loading a world, not merely be available to be
// called: FinalizeLoad is the single place both supported load paths converge.
func TestFinalizeLoad_SeedsPCInputStampAtBoot(t *testing.T) {
	w := buildRestartWorld(t)

	pc := w.Actors["jefferey"]
	if pc.LastPCInputAt == nil {
		t.Fatal("loading a world must seed every PC's idle clock — a nil stamp is what the idle auto-bed reads as 'never idle'")
	}
	if !pc.LastPCInputAt.Equal(w.LoadedAt) {
		t.Errorf("LastPCInputAt = %v, want the world's boot instant %v (one shared restart 'now')", pc.LastPCInputAt, w.LoadedAt)
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
	boot := w.LoadedAt
	sweepAt := boot.Add((sim.DefaultPCIdleSleepMinutes + 1) * time.Minute)

	// The tab is open: the heartbeat stamped presence moments ago. Fresh relative
	// to the SWEEP instant, not to boot — an aged stamp would silently put this
	// PC on the disconnected branch and the test would prove nothing.
	heartbeat := sweepAt.Add(-5 * time.Second)
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["jefferey"].LastPCSeenAt = &heartbeat
		return nil, nil
	}}); err != nil {
		t.Fatalf("stamp presence: %v", err)
	}

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
	sweepAt := w.LoadedAt.Add((sim.DefaultPCIdleSleepMinutes - 1) * time.Minute)

	heartbeat := sweepAt.Add(-5 * time.Second)
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["jefferey"].LastPCSeenAt = &heartbeat
		return nil, nil
	}}); err != nil {
		t.Fatalf("stamp presence: %v", err)
	}

	if _, err := w.Send(sim.AutoBedIdleLodgerPCs(sweepAt)); err != nil {
		t.Fatalf("AutoBedIdleLodgerPCs: %v", err)
	}
	if sleeping(t, w, "jefferey") {
		t.Error("a PC inside the post-boot grace period must not be bedded yet")
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
