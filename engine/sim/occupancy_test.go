package sim_test

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// occupancyAsset builds an occupancy-tracked asset: an unoccupied + an occupied
// tagged state, plus the min-count / night-only knobs.
func occupancyAsset(id sim.AssetID, minCount int, nightOnly bool) *sim.Asset {
	return &sim.Asset{
		ID: id, Name: string(id), Category: "structure", DefaultState: "unoccupied",
		OccupiedMinCount: minCount, OccupiedNightOnly: nightOnly,
		States: []sim.AssetState{
			{ID: 1, State: "unoccupied", Tags: []string{sim.TagUnoccupied}},
			{ID: 2, State: "occupied", Tags: []string{sim.TagOccupied}},
		},
	}
}

// buildOccupancyWorld seeds a world with four structures: a tavern (min 1), a
// workshop (min 2), an inn (night-only), and a barn whose asset has no
// occupied/unoccupied states (not occupancy-tracked). Each structure's
// placement object shares its id (shared-identity bridge). Initial phase = day.
// A capture subscriber is registered before Run.
func buildOccupancyWorld(t *testing.T) (*sim.World, *objEventCapture) {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.Assets.Seed(map[sim.AssetID]*sim.Asset{
		"tavern-a":   occupancyAsset("tavern-a", 1, false),
		"workshop-a": occupancyAsset("workshop-a", 2, false),
		"inn-a":      occupancyAsset("inn-a", 1, true),
		"barn-a": {
			ID: "barn-a", Name: "Barn", Category: "structure", DefaultState: "default",
			States: []sim.AssetState{{ID: 9, State: "default"}},
		},
	})
	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"tavern":   {ID: "tavern", AssetID: "tavern-a", CurrentState: "unoccupied", X: 100, Y: 100},
		"workshop": {ID: "workshop", AssetID: "workshop-a", CurrentState: "unoccupied", X: 200, Y: 200},
		"inn":      {ID: "inn", AssetID: "inn-a", CurrentState: "unoccupied", X: 300, Y: 300},
		"barn":     {ID: "barn", AssetID: "barn-a", CurrentState: "default", X: 400, Y: 400},
	})
	handles.Structures.Seed(map[sim.StructureID]*sim.Structure{
		"tavern":   {ID: "tavern", DisplayName: "Tavern"},
		"workshop": {ID: "workshop", DisplayName: "Workshop"},
		"inn":      {ID: "inn", DisplayName: "Inn"},
		"barn":     {ID: "barn", DisplayName: "Barn"},
	})

	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	cap := &objEventCapture{}
	w.Subscribe(cap) // must precede Run
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go w.Run(ctx)
	// Start in day so the night-only inn test exercises a real day→night flip.
	// Sent AFTER Run starts — Send blocks on the world goroutine consuming it.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Phase = sim.PhaseDay
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed phase: %v", err)
	}
	return w, cap
}

// seedActorInside adds an actor and places it inside structureID (via the index
// chokepoint, so occupancy recomputes). BreakUntil/SleepingUntil optional.
func seedActorInside(t *testing.T, w *sim.World, id sim.ActorID, structureID sim.StructureID, breakUntil, sleepUntil *time.Time) {
	t.Helper()
	_, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a := &sim.Actor{ID: id, DisplayName: string(id), Kind: sim.KindNPCShared, State: sim.StateIdle}
		a.BreakUntil = breakUntil
		a.SleepingUntil = sleepUntil
		world.Actors[id] = a
		sim.SetActorInsideStructure(world, a, structureID)
		return nil, nil
	}})
	if err != nil {
		t.Fatalf("seedActorInside: %v", err)
	}
}

func moveActorInside(t *testing.T, w *sim.World, id sim.ActorID, structureID sim.StructureID) {
	t.Helper()
	_, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		sim.SetActorInsideStructure(world, world.Actors[id], structureID)
		return nil, nil
	}})
	if err != nil {
		t.Fatalf("moveActorInside: %v", err)
	}
}

func objState(w *sim.World, id sim.VillageObjectID) string {
	return w.Published().VillageObjects[id].CurrentState
}

// TestOccupancy_TavernEnterLeave: a single arrival flips the tavern to occupied
// (min 1, not night-only); the departure flips it back.
func TestOccupancy_TavernEnterLeave(t *testing.T) {
	w, _ := buildOccupancyWorld(t)

	seedActorInside(t, w, "patron", "tavern", nil, nil)
	if got := objState(w, "tavern"); got != "occupied" {
		t.Fatalf("after arrival, tavern = %q, want occupied", got)
	}

	moveActorInside(t, w, "patron", "") // leave
	if got := objState(w, "tavern"); got != "unoccupied" {
		t.Fatalf("after departure, tavern = %q, want unoccupied", got)
	}
}

// TestOccupancy_WorkshopMinCount: occupied requires >= 2 inside.
func TestOccupancy_WorkshopMinCount(t *testing.T) {
	w, _ := buildOccupancyWorld(t)

	seedActorInside(t, w, "smith-1", "workshop", nil, nil)
	if got := objState(w, "workshop"); got != "unoccupied" {
		t.Fatalf("one worker, workshop = %q, want unoccupied (min 2)", got)
	}
	seedActorInside(t, w, "smith-2", "workshop", nil, nil)
	if got := objState(w, "workshop"); got != "occupied" {
		t.Fatalf("two workers, workshop = %q, want occupied", got)
	}
}

// TestOccupancy_InnNightOnly: a guest inside by day leaves the inn unoccupied;
// the day→night transition flips it occupied via the phase sweep; night→day
// flips it back with no one moving.
func TestOccupancy_InnNightOnly(t *testing.T) {
	w, _ := buildOccupancyWorld(t)

	seedActorInside(t, w, "guest", "inn", nil, nil)
	if got := objState(w, "inn"); got != "unoccupied" {
		t.Fatalf("guest inside by day, inn = %q, want unoccupied (night-only)", got)
	}

	if _, err := w.Send(sim.ApplyPhaseTransition(sim.PhaseNight)); err != nil {
		t.Fatalf("transition night: %v", err)
	}
	if got := objState(w, "inn"); got != "occupied" {
		t.Fatalf("after dusk, inn = %q, want occupied", got)
	}

	if _, err := w.Send(sim.ApplyPhaseTransition(sim.PhaseDay)); err != nil {
		t.Fatalf("transition day: %v", err)
	}
	if got := objState(w, "inn"); got != "unoccupied" {
		t.Fatalf("after dawn, inn = %q, want unoccupied", got)
	}
}

// TestOccupancy_CountsBreakSleepingForNow documents the accepted v2 behavior:
// because v2 has no take_break/sleep lifecycle yet (nothing transitions
// BreakUntil/SleepingUntil at runtime), occupancy counts every present actor
// regardless of those fields. The v1-style exclusion — plus a recompute trigger
// on the wake/end-break transition — lands when that lifecycle is ported, so
// the count never goes stale waiting on a timer with no setter to fire from.
func TestOccupancy_CountsBreakSleepingForNow(t *testing.T) {
	w, _ := buildOccupancyWorld(t)
	future := time.Now().Add(time.Hour)

	// An actor flagged on-break is still physically present, so it counts (min 1).
	seedActorInside(t, w, "on-break", "tavern", &future, nil)
	if got := objState(w, "tavern"); got != "occupied" {
		t.Fatalf("present actor (on break) should count: tavern = %q, want occupied", got)
	}
}

// TestOccupancy_NonTrackedAssetNoOp: a structure whose asset has no
// occupied/unoccupied states is not occupancy-tracked — arrivals don't flip it
// and emit no VillageObjectStateChanged for it.
func TestOccupancy_NonTrackedAssetNoOp(t *testing.T) {
	w, cap := buildOccupancyWorld(t)

	seedActorInside(t, w, "hand", "barn", nil, nil)
	if got := objState(w, "barn"); got != "default" {
		t.Fatalf("barn = %q, want default (not occupancy-tracked)", got)
	}
	for _, evt := range cap.snapshot() {
		if sc, ok := evt.(*sim.VillageObjectStateChanged); ok && sc.ObjectID == "barn" {
			t.Errorf("unexpected VillageObjectStateChanged for non-tracked barn: %+v", sc)
		}
	}
}

// TestOccupancy_EmitsStateChange: a flip emits VillageObjectStateChanged so the
// client gets an object_state_changed frame.
func TestOccupancy_EmitsStateChange(t *testing.T) {
	w, cap := buildOccupancyWorld(t)

	seedActorInside(t, w, "patron", "tavern", nil, nil)

	var found *sim.VillageObjectStateChanged
	for _, evt := range cap.snapshot() {
		if sc, ok := evt.(*sim.VillageObjectStateChanged); ok && sc.ObjectID == "tavern" {
			found = sc
		}
	}
	if found == nil {
		t.Fatal("no VillageObjectStateChanged emitted for the tavern flip")
	}
	if found.ToState != "occupied" || found.FromState != "unoccupied" {
		t.Errorf("event = %s->%s, want unoccupied->occupied", found.FromState, found.ToState)
	}
}
