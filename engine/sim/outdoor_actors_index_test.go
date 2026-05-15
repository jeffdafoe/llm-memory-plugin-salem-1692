package sim_test

import (
	"context"
	"sort"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// outdoor_actors_index_test.go — outdoorActors secondary-index
// maintenance tests. Pairs with the actorsByStructure tests in
// locomotion_ticker_test.go — the two indices must move in lockstep
// through every setActorInsideStructure call and rebuildIndices.
//
// Covers two paths: rebuild from primary state (LoadWorld) and runtime
// maintenance (setActorInsideStructure transitions).

// TestOutdoorActorsIndex_RebuildIndices covers LoadWorld's rebuild
// path: seeding actors with a mix of indoor / outdoor attribution and
// asserting outdoorActors contains exactly the outdoor population
// after LoadWorld.
func TestOutdoorActorsIndex_RebuildIndices(t *testing.T) {
	repo, h := mem.NewRepository()
	h.Structures.Seed(map[sim.StructureID]*sim.Structure{
		"tavern":  {ID: "tavern", DisplayName: "Tavern"},
		"cottage": {ID: "cottage", DisplayName: "Cottage"},
	})
	h.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"outdoor-1": {ID: "outdoor-1"},                                 // outdoor (InsideStructureID empty)
		"outdoor-2": {ID: "outdoor-2"},                                 // outdoor
		"indoor-1":  {ID: "indoor-1", InsideStructureID: "tavern"},     // indoor
		"indoor-2":  {ID: "indoor-2", InsideStructureID: "cottage"},    // indoor
		"outdoor-3": {ID: "outdoor-3", CurrentHuddleID: "some-huddle"}, // outdoor, in huddle — still outdoor for index
	})

	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}

	got := sim.OutdoorActorIDs(w)
	sort.Slice(got, func(i, j int) bool { return got[i] < got[j] })
	want := []sim.ActorID{"outdoor-1", "outdoor-2", "outdoor-3"}
	if len(got) != len(want) {
		t.Fatalf("OutdoorActorIDs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("OutdoorActorIDs[%d] = %q, want %q", i, got[i], want[i])
		}
	}

	// Indoor actors must NOT be in the outdoor index.
	for _, id := range []sim.ActorID{"indoor-1", "indoor-2"} {
		for _, gotID := range got {
			if gotID == id {
				t.Errorf("indoor actor %q leaked into outdoorActors", id)
			}
		}
	}
}

// TestOutdoorActorsIndex_SetActorInsideStructureMaintenance covers the
// runtime mutation chokepoint: setActorInsideStructure must move actors
// between outdoorActors and actorsByStructure in lockstep with the
// InsideStructureID field. Drives the four transitions: outdoor→indoor,
// indoor→indoor (different structure), indoor→outdoor, and no-op
// (same structure).
func TestOutdoorActorsIndex_SetActorInsideStructureMaintenance(t *testing.T) {
	repo, h := mem.NewRepository()
	h.Structures.Seed(map[sim.StructureID]*sim.Structure{
		"tavern":  {ID: "tavern"},
		"cottage": {ID: "cottage"},
	})
	h.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"ann": {ID: "ann"},                               // outdoors
		"ben": {ID: "ben", InsideStructureID: "tavern"},  // indoors
		"cal": {ID: "cal", InsideStructureID: "cottage"}, // indoors elsewhere
	})

	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	// Baseline: ann outdoor, ben + cal indoor.
	assertOutdoorActorIDs(t, w, []sim.ActorID{"ann"}, "baseline")

	// (1) outdoor→indoor: ann enters tavern.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		sim.SetActorInsideStructure(world, world.Actors["ann"], "tavern")
		return nil, nil
	}}); err != nil {
		t.Fatalf("ann→tavern: %v", err)
	}
	assertOutdoorActorIDs(t, w, nil, "after ann enters tavern")
	if got := sim.ActorsInStructure(w, "tavern"); !containsID(got, "ann") || !containsID(got, "ben") {
		t.Errorf("after ann enters tavern, ActorsInStructure[tavern] = %v, want both ann and ben", got)
	}

	// (2) indoor→indoor (different structure): ann moves to cottage.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		sim.SetActorInsideStructure(world, world.Actors["ann"], "cottage")
		return nil, nil
	}}); err != nil {
		t.Fatalf("ann→cottage: %v", err)
	}
	assertOutdoorActorIDs(t, w, nil, "after ann moves indoor→indoor")
	if got := sim.ActorsInStructure(w, "tavern"); containsID(got, "ann") {
		t.Errorf("after ann leaves tavern, tavern still lists ann: %v", got)
	}
	if got := sim.ActorsInStructure(w, "cottage"); !containsID(got, "ann") {
		t.Errorf("after ann enters cottage, cottage should list ann: %v", got)
	}

	// (3) indoor→outdoor: ben exits tavern.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		sim.SetActorInsideStructure(world, world.Actors["ben"], "")
		return nil, nil
	}}); err != nil {
		t.Fatalf("ben→outdoor: %v", err)
	}
	assertOutdoorActorIDs(t, w, []sim.ActorID{"ben"}, "after ben exits tavern")
	if got := sim.ActorsInStructure(w, "tavern"); containsID(got, "ben") {
		t.Errorf("after ben exits tavern, tavern still lists ben: %v", got)
	}

	// (4) no-op (same structure): cal stays in cottage. No index change.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		sim.SetActorInsideStructure(world, world.Actors["cal"], "cottage")
		return nil, nil
	}}); err != nil {
		t.Fatalf("cal→cottage (no-op): %v", err)
	}
	assertOutdoorActorIDs(t, w, []sim.ActorID{"ben"}, "after no-op")

	// (5) outdoor→outdoor (no-op): ben stays outside.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		sim.SetActorInsideStructure(world, world.Actors["ben"], "")
		return nil, nil
	}}); err != nil {
		t.Fatalf("ben→outdoor (no-op): %v", err)
	}
	assertOutdoorActorIDs(t, w, []sim.ActorID{"ben"}, "after outdoor no-op")
}

// assertOutdoorActorIDs reads OutdoorActorIDs and compares the sorted
// result against want (also sorted).
func assertOutdoorActorIDs(t *testing.T, w *sim.World, want []sim.ActorID, stage string) {
	t.Helper()
	got := sim.OutdoorActorIDs(w)
	sort.Slice(got, func(i, j int) bool { return got[i] < got[j] })
	wantCopy := append([]sim.ActorID(nil), want...)
	sort.Slice(wantCopy, func(i, j int) bool { return wantCopy[i] < wantCopy[j] })
	if len(got) != len(wantCopy) {
		t.Errorf("[%s] OutdoorActorIDs = %v, want %v", stage, got, wantCopy)
		return
	}
	for i := range wantCopy {
		if got[i] != wantCopy[i] {
			t.Errorf("[%s] OutdoorActorIDs[%d] = %q, want %q", stage, i, got[i], wantCopy[i])
		}
	}
}

func containsID(ids []sim.ActorID, want sim.ActorID) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}
