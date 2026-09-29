package perception

import (
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// TestUnattributableSourceNeverSteers — LLM-682. Arrival resolves only a named
// object (sim.VillageObject.Attributable), so no cue may send an actor to an
// unnamed one: the owned-bush steer, the free food/water list and the free rest
// spots each drop it, and each keeps the same source once it has a name.
func TestUnattributableSourceNeverSteers(t *testing.T) {
	unnamed := func(o *sim.VillageObject) *sim.VillageObject {
		o.DisplayName = ""
		return o
	}

	// "## Your bushes to harvest": her only bush has no name.
	subj := &sim.ActorSnapshot{Inventory: map[sim.ItemKind]int{"raspberries": 2}, RestockPolicy: foragePolicy("raspberries", 10),
		KnownPlaces: remembersGather("raspberries", "bushA")}
	snap := &sim.Snapshot{
		Actors:            map[sim.ActorID]*sim.ActorSnapshot{"prudence": subj},
		VillageObjects:    map[sim.VillageObjectID]*sim.VillageObject{"bushA": unnamed(forageBush("prudence", "raspberries", 10))},
		RestockReorderPct: 25,
	}
	if v := buildForage(snap, "prudence", subj, false); v != nil && len(v.Items) > 0 {
		t.Errorf("an unnamed bush was steered to: %+v", v.Items)
	}
	snap.VillageObjects["bushA"] = forageBush("prudence", "raspberries", 10)
	if v := buildForage(snap, "prudence", subj, false); v == nil || len(v.Items) != 1 || v.Items[0].MoveHandle != "bushA" {
		t.Errorf("the same bush with a name: got %+v, want it as the move handle", v)
	}

	// The free food/water list and the free rest spots.
	walker := &sim.ActorSnapshot{Pos: sim.GridPoint{X: 10, Y: 10}}
	for _, tc := range []struct {
		name  string
		need  sim.NeedKey
		count func(*sim.Snapshot) int
	}{
		{"free satiation source", "hunger", func(s *sim.Snapshot) int {
			return len(gatherFreeSatiationSources(s, "walker", walker, "hunger"))
		}},
		{"free rest spot", recoveryTirednessNeed, func(s *sim.Snapshot) int {
			return len(gatherFreeRestSpots(s, walker))
		}},
	} {
		src := &sim.VillageObject{ID: "src", Pos: sim.WorldPos{X: 200, Y: 200},
			Refreshes: []*sim.ObjectRefresh{{Attribute: tc.need, Amount: -5}}}
		s := &sim.Snapshot{VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{"src": src}}
		if got := tc.count(s); got != 0 {
			t.Errorf("%s: an unnamed source was listed (%d)", tc.name, got)
		}
		src.DisplayName = "Berry Bush"
		if got := tc.count(s); got != 1 {
			t.Errorf("%s: the named source listed %d times, want 1", tc.name, got)
		}
	}

	// Standing on the source: the "a source is here" checks that hold the commute
	// and the no-food dead end back.
	src := &sim.VillageObject{ID: "src", Pos: sim.WorldPos{X: 200, Y: 200},
		Refreshes: []*sim.ObjectRefresh{{Attribute: "hunger", Amount: -5}}}
	hungry := &sim.ActorSnapshot{Pos: objectLoiterPin(src), Needs: map[sim.NeedKey]int{"hunger": 99}}
	s := &sim.Snapshot{VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{"src": src}}
	if consumableSourceColocated(s, "walker", hungry, "hunger") || atResolvableSatiationSource(s, "walker", hungry) {
		t.Error("an unnamed source underfoot counts as usable here")
	}
	src.DisplayName = "Berry Bush"
	if !consumableSourceColocated(s, "walker", hungry, "hunger") || !atResolvableSatiationSource(s, "walker", hungry) {
		t.Error("the named source underfoot does not count")
	}
}
