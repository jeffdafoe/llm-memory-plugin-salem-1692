package perception

import (
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// seek_work_places_test.go — LLM-152/160. The directional half of seek-work:
// Build lists the town's businesses (village objects tagged sim.TagBusiness,
// resolved to their structure names) as move_to destinations. LLM-160 made this a
// STANDING cue for a broke idle worker with no solicitable employer present —
// every tick, not gated on a seek-work warrant — so move_to always has a real,
// resolvable target instead of an invented place name.

func TestBuildSeekWorkPlaces(t *testing.T) {
	// Business objects share their id with the co-located structure (the identity
	// bridge), so each resolves to the structure's clean DisplayName. "stall" has
	// no matching structure and falls back to its own DisplayName; the residence
	// carries no business tag and is excluded.
	snap := &sim.Snapshot{
		Structures: map[sim.StructureID]*sim.Structure{
			"tav": {DisplayName: "Tavern"},
			"smy": {DisplayName: "Blacksmith"},
			"res": {DisplayName: "Walker Residence"},
			"frm": {DisplayName: "Ellis Farm"},
		},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			"tav":   {ID: "tav", Tags: []string{"business", "lodging", "tavern"}},
			"smy":   {ID: "smy", Tags: []string{"business", "smithy"}},
			"res":   {ID: "res", Tags: []string{}},                           // residence — not a business
			"frm":   {ID: "frm", Tags: []string{"market_stall", "business"}}, // a farm
			"stall": {ID: "stall", DisplayName: "Lone Stall", Tags: []string{"business"}},
		},
	}
	got := buildSeekWorkPlaces(snap)
	want := []string{"Blacksmith", "Ellis Farm", "Lone Stall", "Tavern"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("buildSeekWorkPlaces = %v, want %v (sorted, de-duped, business-only)", got, want)
	}
}

func TestBuildSeekWorkPlaces_DedupNilAndNoneTagged(t *testing.T) {
	// Two business objects resolving to the same name collapse to one entry.
	dup := &sim.Snapshot{
		Structures: map[sim.StructureID]*sim.Structure{"a": {DisplayName: "General Store"}},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			"a":   {ID: "a", Tags: []string{"business"}},
			"dup": {ID: "dup", DisplayName: "General Store", Tags: []string{"business"}},
		},
	}
	if got := buildSeekWorkPlaces(dup); len(got) != 1 || got[0] != "General Store" {
		t.Errorf("dedup: buildSeekWorkPlaces = %v, want [General Store]", got)
	}

	if got := buildSeekWorkPlaces(nil); got != nil {
		t.Errorf("nil snapshot: want nil, got %v", got)
	}

	none := &sim.Snapshot{VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
		"x": {ID: "x", Tags: []string{"lodging"}},
	}}
	if got := buildSeekWorkPlaces(none); got != nil {
		t.Errorf("no business objects: want nil, got %v", got)
	}
}

// TestBuild_SeekWorkPlacesStandingForBrokeWorker proves the wiring end-to-end
// (LLM-160): Build populates SeekWorkPlaces for a broke idle worker with no
// solicitable employer present — every tick, no seek-work warrant required — and
// leaves it empty for a worker that holds coin or for a non-worker.
func TestBuild_SeekWorkPlacesStandingForBrokeWorker(t *testing.T) {
	worker := func(coins int) *sim.ActorSnapshot {
		a := actorSnap(sim.StateIdle, "", 0, 0, "", coins)
		a.AttributeSlugs = []string{sim.AttrWorker}
		return a
	}
	mk := func(subj *sim.ActorSnapshot) *sim.Snapshot {
		return &sim.Snapshot{
			Actors:     map[sim.ActorID]*sim.ActorSnapshot{"lewis": subj},
			Structures: map[sim.StructureID]*sim.Structure{"tav": {DisplayName: "Tavern"}},
			VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
				"tav": {ID: "tav", Tags: []string{"business"}},
			},
		}
	}

	// Broke worker, no one present to hire it → directory, no warrant needed.
	if p := Build(mk(worker(0)), "lewis", nil); len(p.SeekWorkPlaces) != 1 || p.SeekWorkPlaces[0] != "Tavern" {
		t.Errorf("broke worker alone: SeekWorkPlaces = %v, want [Tavern]", p.SeekWorkPlaces)
	}

	// Same worker holding coin → not broke → no directory.
	if p := Build(mk(worker(5)), "lewis", nil); len(p.SeekWorkPlaces) != 0 {
		t.Errorf("worker with coin: SeekWorkPlaces = %v, want empty", p.SeekWorkPlaces)
	}

	// A broke NON-worker (no worker attribute) is not directed to seek work.
	if p := Build(mk(actorSnap(sim.StateIdle, "", 0, 0, "", 0)), "lewis", nil); len(p.SeekWorkPlaces) != 0 {
		t.Errorf("non-worker: SeekWorkPlaces = %v, want empty", p.SeekWorkPlaces)
	}
}

func TestRenderSeekWorkPlaces(t *testing.T) {
	var b strings.Builder
	renderSeekWorkPlaces(&b, []string{"Blacksmith", "Ellis Farm", "Tavern"})
	out := b.String()
	for _, want := range []string{"take paid work", "move_to", "Blacksmith", "Ellis Farm", "Tavern"} {
		if !strings.Contains(out, want) {
			t.Errorf("renderSeekWorkPlaces missing %q\n%s", want, out)
		}
	}

	// Content-gated: an empty list renders nothing.
	var empty strings.Builder
	renderSeekWorkPlaces(&empty, nil)
	if empty.String() != "" {
		t.Errorf("empty list: want no output, got %q", empty.String())
	}
}
