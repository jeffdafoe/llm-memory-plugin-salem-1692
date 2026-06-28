package perception

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// seek_work_places_test.go — LLM-152/160/155. The directional half of seek-work:
// Build lists the town's businesses (village objects tagged sim.TagBusiness,
// resolved to their structure names) as move_to destinations. LLM-160 made this a
// STANDING cue for a broke idle worker with no solicitable employer present —
// every tick, not gated on a seek-work warrant — so move_to always has a real,
// resolvable target instead of an invented place name. LLM-155 added a qualitative
// distance + direction per entry, ordered nearest-first, and drops a business the
// worker recently found shut.

func TestBuildSeekWorkPlaces(t *testing.T) {
	// Business objects share their id with the co-located structure (the identity
	// bridge), so each resolves to the structure's clean DisplayName. "stall" has
	// no matching structure and falls back to its own DisplayName; the residence
	// carries no business tag and is excluded. Each object's WorldPos sets its
	// distance from the actor (at the world-origin tile): the list comes back
	// nearest-first with the eat/drink cue's qualitative distance + direction.
	actor := &sim.ActorSnapshot{Pos: sim.WorldToTile(0, 0)}
	snap := &sim.Snapshot{
		Structures: map[sim.StructureID]*sim.Structure{
			"tav": {DisplayName: "Tavern"},
			"smy": {DisplayName: "Blacksmith"},
			"res": {DisplayName: "Walker Residence"},
			"frm": {DisplayName: "Ellis Farm"},
		},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			"tav":   {ID: "tav", Pos: sim.WorldPos{X: 32, Y: 0}, Tags: []string{"business", "lodging", "tavern"}},         // 1 tile E
			"smy":   {ID: "smy", Pos: sim.WorldPos{X: 320, Y: 0}, Tags: []string{"business", "smithy"}},                   // 10 tiles E
			"res":   {ID: "res", Pos: sim.WorldPos{X: 0, Y: 0}, Tags: []string{}},                                         // residence — not a business
			"frm":   {ID: "frm", Pos: sim.WorldPos{X: 0, Y: 3200}, Tags: []string{"market_stall", "business"}},            // 100 tiles S (a far farm)
			"stall": {ID: "stall", DisplayName: "Lone Stall", Pos: sim.WorldPos{X: 0, Y: 64}, Tags: []string{"business"}}, // 2 tiles S
		},
	}
	got := buildSeekWorkPlaces(snap, actor)
	want := []SeekWorkPlace{
		{Name: "Tavern", Distance: "right nearby", Direction: "east"},
		{Name: "Lone Stall", Distance: "right nearby", Direction: "south"},
		{Name: "Blacksmith", Distance: "a short walk", Direction: "east"},
		{Name: "Ellis Farm", Distance: "a long walk", Direction: "south"}, // far farm still listed, with a far descriptor
	}
	if len(got) != len(want) {
		t.Fatalf("buildSeekWorkPlaces len = %d, want %d (%+v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i].Name != want[i].Name || got[i].Distance != want[i].Distance || got[i].Direction != want[i].Direction {
			t.Errorf("entry %d = {%q %q %q}, want {%q %q %q}",
				i, got[i].Name, got[i].Distance, got[i].Direction, want[i].Name, want[i].Distance, want[i].Direction)
		}
	}
}

func TestBuildSeekWorkPlaces_DedupNilAndNoneTagged(t *testing.T) {
	actor := &sim.ActorSnapshot{Pos: sim.WorldToTile(0, 0)}

	// Two business objects resolving to the same name collapse to one entry — the
	// NEAREST representative (LLM-155). "a" resolves via Structures (10 tiles);
	// "dup" falls back to its own DisplayName (1 tile), so the near one wins and
	// the kept entry carries its distance.
	dup := &sim.Snapshot{
		Structures: map[sim.StructureID]*sim.Structure{"a": {DisplayName: "General Store"}},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			"a":   {ID: "a", Pos: sim.WorldPos{X: 320, Y: 0}, Tags: []string{"business"}},
			"dup": {ID: "dup", DisplayName: "General Store", Pos: sim.WorldPos{X: 32, Y: 0}, Tags: []string{"business"}},
		},
	}
	if got := buildSeekWorkPlaces(dup, actor); len(got) != 1 || got[0].Name != "General Store" || got[0].Distance != "right nearby" {
		t.Errorf("dedup: buildSeekWorkPlaces = %+v, want one {General Store, right nearby}", got)
	}

	if got := buildSeekWorkPlaces(nil, actor); got != nil {
		t.Errorf("nil snapshot: want nil, got %+v", got)
	}

	if got := buildSeekWorkPlaces(&sim.Snapshot{}, nil); got != nil {
		t.Errorf("nil actorSnap: want nil, got %+v", got)
	}

	none := &sim.Snapshot{VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
		"x": {ID: "x", Tags: []string{"lodging"}},
	}}
	if got := buildSeekWorkPlaces(none, actor); got != nil {
		t.Errorf("no business objects: want nil, got %+v", got)
	}
}

// TestBuildSeekWorkPlaces_DropsRememberedShut proves a business the worker has an
// earned ObservedClosed memory of (within its TTL) is DROPPED from the directory,
// not annotated — the worker shouldn't be sent back to a door it just found shut
// (LLM-155). The other business, never visited, stays.
func TestBuildSeekWorkPlaces_DropsRememberedShut(t *testing.T) {
	now := time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC)
	actor := &sim.ActorSnapshot{
		Pos: sim.WorldToTile(0, 0),
		Observed: sim.NewObservedStates(map[sim.ObservedStateKey]time.Time{
			{StructureID: "tav", Condition: sim.ObservedClosed}: now.Add(-time.Hour), // found shut an hour ago, within the 4h TTL
		}),
	}
	snap := &sim.Snapshot{
		PublishedAt: now,
		Structures: map[sim.StructureID]*sim.Structure{
			"tav": {DisplayName: "Tavern"},
			"smy": {DisplayName: "Blacksmith"},
		},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			"tav": {ID: "tav", Pos: sim.WorldPos{X: 32, Y: 0}, Tags: []string{"business"}},
			"smy": {ID: "smy", Pos: sim.WorldPos{X: 320, Y: 0}, Tags: []string{"business"}},
		},
	}
	got := buildSeekWorkPlaces(snap, actor)
	if len(got) != 1 || got[0].Name != "Blacksmith" {
		t.Errorf("remembered-shut Tavern should be dropped: got %+v, want only Blacksmith", got)
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
	if p := Build(mk(worker(0)), "lewis", nil); len(p.SeekWorkPlaces) != 1 || p.SeekWorkPlaces[0].Name != "Tavern" {
		t.Errorf("broke worker alone: SeekWorkPlaces = %+v, want [Tavern]", p.SeekWorkPlaces)
	}

	// Same worker holding coin → not broke → no directory.
	if p := Build(mk(worker(5)), "lewis", nil); len(p.SeekWorkPlaces) != 0 {
		t.Errorf("worker with coin: SeekWorkPlaces = %+v, want empty", p.SeekWorkPlaces)
	}

	// A broke NON-worker (no worker attribute) is not directed to seek work.
	if p := Build(mk(actorSnap(sim.StateIdle, "", 0, 0, "", 0)), "lewis", nil); len(p.SeekWorkPlaces) != 0 {
		t.Errorf("non-worker: SeekWorkPlaces = %+v, want empty", p.SeekWorkPlaces)
	}

	// A broke worker mid source-activity is NOT free to leave → no directory, so the
	// directive bit stays off and the owed-reply nag is preserved (LLM-160 review).
	busy := worker(0)
	busy.SourceActivityKind = sim.SourceActivityHarvest
	if p := Build(mk(busy), "lewis", nil); len(p.SeekWorkPlaces) != 0 {
		t.Errorf("broke worker mid source-activity: SeekWorkPlaces = %+v, want empty", p.SeekWorkPlaces)
	}
}

func TestRenderSeekWorkPlaces(t *testing.T) {
	var b strings.Builder
	renderSeekWorkPlaces(&b, []SeekWorkPlace{
		{Name: "Tavern", Distance: "right nearby", Direction: "east"},
		{Name: "Blacksmith", Distance: "a short walk", Direction: "east"},
		{Name: "Ellis Farm", Distance: "a long walk", Direction: "south"},
	})
	out := b.String()
	for _, want := range []string{
		"take paid work", "move_to",
		"- Tavern — right nearby east",
		"- Blacksmith — a short walk east",
		"- Ellis Farm — a long walk south",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("renderSeekWorkPlaces missing %q\n%s", want, out)
		}
	}

	// A place with no distance (coincident / unresolved) renders the bare name.
	var bare strings.Builder
	renderSeekWorkPlaces(&bare, []SeekWorkPlace{{Name: "Tavern"}})
	if got := bare.String(); !strings.Contains(got, "- Tavern\n") {
		t.Errorf("bare name: want a \"- Tavern\" line, got %q", got)
	}

	// Content-gated: an empty list renders nothing.
	var empty strings.Builder
	renderSeekWorkPlaces(&empty, nil)
	if empty.String() != "" {
		t.Errorf("empty list: want no output, got %q", empty.String())
	}
}
