package perception

import (
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// seek_work_places_test.go — LLM-152. The directional half of the seek-work
// backstop: when a broke worker is nudged to go earn (a seek_work warrant in the
// batch), Build lists the town's businesses (village objects tagged
// sim.TagBusiness, resolved to their structure names) as move_to destinations.

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

func TestHasSeekWorkWarrant(t *testing.T) {
	if hasSeekWorkWarrant(nil) {
		t.Error("nil warrants: want false")
	}
	if hasSeekWorkWarrant([]sim.WarrantMeta{{Reason: sim.IdleBackstopWarrantReason{}}}) {
		t.Error("no seek_work warrant: want false")
	}
	mixed := []sim.WarrantMeta{
		{Reason: sim.IdleBackstopWarrantReason{}},
		{Reason: sim.SeekWorkWarrantReason{}},
	}
	if !hasSeekWorkWarrant(mixed) {
		t.Error("seek_work warrant present: want true")
	}
}

// TestBuild_SeekWorkPlacesGatedOnWarrant proves the wiring end-to-end: Build
// populates SeekWorkPlaces only when a real SeekWorkWarrantReason is in the
// batch, even though the businesses exist either way.
func TestBuild_SeekWorkPlacesGatedOnWarrant(t *testing.T) {
	mk := func() *sim.Snapshot {
		return &sim.Snapshot{
			Actors:     map[sim.ActorID]*sim.ActorSnapshot{"lewis": actorSnap(sim.StateIdle, "", 0, 0, "", 0)},
			Structures: map[sim.StructureID]*sim.Structure{"tav": {DisplayName: "Tavern"}},
			VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
				"tav": {ID: "tav", Tags: []string{"business"}},
			},
		}
	}

	// A real seek_work warrant (not a generic BasicWarrantReason) populates it.
	seek := sim.WarrantMeta{Reason: sim.SeekWorkWarrantReason{}}
	if p := Build(mk(), "lewis", []sim.WarrantMeta{seek}); len(p.SeekWorkPlaces) != 1 || p.SeekWorkPlaces[0] != "Tavern" {
		t.Errorf("with seek_work warrant: SeekWorkPlaces = %v, want [Tavern]", p.SeekWorkPlaces)
	}

	// An unrelated warrant leaves the list empty even though the business exists.
	other := basicWarrant(sim.WarrantKindArrived, 5, "", "", "lewis")
	if p := Build(mk(), "lewis", []sim.WarrantMeta{other}); len(p.SeekWorkPlaces) != 0 {
		t.Errorf("without seek_work warrant: SeekWorkPlaces = %v, want empty", p.SeekWorkPlaces)
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
