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

// TestOccupancy_RestingExcludedNonNightOnly verifies option (b) (ZBBS-HOME-284 #2):
// in a non-night-only structure (tavern = open-for-business), a sleeping or
// on-break keeper does NOT count, so the structure darkens — the home==work
// vendor case. In a night-only structure (inn = guests lodging) everyone counts,
// so a sleeping guest keeps it lit at night.
func TestOccupancy_RestingExcludedNonNightOnly(t *testing.T) {
	w, _ := buildOccupancyWorld(t)
	future := time.Now().Add(time.Hour)

	// Tavern (non-night-only, min 1): a sleeping keeper doesn't count → dark.
	seedActorInside(t, w, "keeper", "tavern", nil, &future)
	if got := objState(w, "tavern"); got != "unoccupied" {
		t.Fatalf("sleeping keeper should not count: tavern = %q, want unoccupied", got)
	}

	// On-break also excluded for a non-night-only structure.
	seedActorInside(t, w, "breaker", "workshop", &future, nil)
	if got := objState(w, "workshop"); got != "unoccupied" {
		t.Fatalf("on-break actor should not count: workshop = %q, want unoccupied", got)
	}

	// Inn (night-only): a sleeping guest DOES count. Lit at night.
	seedActorInside(t, w, "guest", "inn", nil, &future)
	if _, err := w.Send(sim.ApplyPhaseTransition(sim.PhaseNight)); err != nil {
		t.Fatalf("transition night: %v", err)
	}
	if got := objState(w, "inn"); got != "occupied" {
		t.Fatalf("sleeping guest in night-only inn should count: inn = %q, want occupied", got)
	}
}

// TestOccupancy_HomeWorkKeeperDarkensOnSleep is the end-to-end recompute-trigger
// check: a home==work tavern keeper bedded by the sleep backstop darkens the
// tavern (executeNPCSleep → refresh), and waking re-lights it (wakeNPC → refresh).
func TestOccupancy_HomeWorkKeeperDarkensOnSleep(t *testing.T) {
	w, _ := buildOccupancyWorld(t)

	// Awake home==work keeper inside the tavern (unscheduled → always off-shift).
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a := &sim.Actor{ID: "keeper", DisplayName: "Keeper", Kind: sim.KindNPCStateful, State: sim.StateIdle, HomeStructureID: "tavern"}
		world.Actors["keeper"] = a
		sim.SetActorInsideStructure(world, a, "tavern")
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed keeper: %v", err)
	}
	if got := objState(w, "tavern"); got != "occupied" {
		t.Fatalf("awake keeper present: tavern = %q, want occupied", got)
	}

	// Backstop beds the off-shift at-home keeper → tavern darkens.
	if _, err := w.Send(sim.AutoBedAtHomeNPCs(time.Now().UTC())); err != nil {
		t.Fatalf("auto-bed: %v", err)
	}
	if got := objState(w, "tavern"); got != "unoccupied" {
		t.Fatalf("keeper asleep: tavern = %q, want unoccupied", got)
	}

	// Expire the sleep cap, run the wake sweep → tavern re-lights.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		past := time.Now().Add(-time.Minute)
		world.Actors["keeper"].SleepingUntil = &past
		return nil, nil
	}}); err != nil {
		t.Fatalf("expire sleep: %v", err)
	}
	if _, err := w.Send(sim.WakeExpiredNPCSleepers(time.Now().UTC())); err != nil {
		t.Fatalf("wake: %v", err)
	}
	if got := objState(w, "tavern"); got != "occupied" {
		t.Fatalf("keeper awake again: tavern = %q, want occupied", got)
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
