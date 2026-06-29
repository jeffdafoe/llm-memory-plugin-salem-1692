package perception

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// seek_work_places_test.go — LLM-152/160/155/168. The directional half of seek-work:
// Build lists the town's businesses (village objects tagged sim.TagBusiness,
// resolved to their structure names) as move_to destinations. LLM-160 made this a
// STANDING cue for a workless idle worker with no solicitable employer present —
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

	// Equal-distance duplicates resolve deterministically: same label, same
	// distance, different directions → the lowest object id wins, so the kept
	// direction can't flip with map iteration order (LLM-155). "store_a" is 1 tile
	// east, "store_b" 1 tile west; store_a's id sorts first, so "east" must win
	// across repeated builds (Go randomizes map iteration per range).
	tie := &sim.Snapshot{
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			"store_a": {ID: "store_a", DisplayName: "General Store", Pos: sim.WorldPos{X: 32, Y: 0}, Tags: []string{"business"}},
			"store_b": {ID: "store_b", DisplayName: "General Store", Pos: sim.WorldPos{X: -32, Y: 0}, Tags: []string{"business"}},
		},
	}
	for i := 0; i < 8; i++ {
		got := buildSeekWorkPlaces(tie, actor)
		if len(got) != 1 || got[0].Direction != "east" {
			t.Fatalf("equal-distance dedup not deterministic: got %+v, want one entry, direction east (lowest id store_a)", got)
		}
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

// TestBuild_SeekWorkPlacesStandingForWorklessWorker proves the wiring end-to-end
// (LLM-160/168): Build populates SeekWorkPlaces for a WORKLESS idle worker with no
// solicitable employer present — every tick, no seek-work warrant required, and
// whether or not it holds coin — and leaves it empty for a worker that has a post
// of its own or for a non-worker.
func TestBuild_SeekWorkPlacesStandingForWorklessWorker(t *testing.T) {
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

	// Workless worker, broke, no one present to hire it → directory, no warrant needed.
	if p := Build(mk(worker(0)), "lewis", nil); len(p.SeekWorkPlaces) != 1 || p.SeekWorkPlaces[0].Name != "Tavern" {
		t.Errorf("workless worker (broke) alone: SeekWorkPlaces = %+v, want [Tavern]", p.SeekWorkPlaces)
	}

	// LLM-168: the same workless worker holding coin STILL gets the directory — a
	// worker with no post has nothing else to do on-shift whether or not it's broke.
	if p := Build(mk(worker(15)), "lewis", nil); len(p.SeekWorkPlaces) != 1 || p.SeekWorkPlaces[0].Name != "Tavern" {
		t.Errorf("workless worker with coin: SeekWorkPlaces = %+v, want [Tavern]", p.SeekWorkPlaces)
	}

	// A worker WITH a RESOLVABLE post of its own (work_structure_id naming a structure
	// in the snapshot) is steered there by the duty steer, not the seek-work directory
	// (LLM-168). "tav" resolves via mk's Structures.
	homed := worker(0)
	homed.WorkStructureID = "tav"
	if p := Build(mk(homed), "lewis", nil); len(p.SeekWorkPlaces) != 0 {
		t.Errorf("worker with a workplace: SeekWorkPlaces = %+v, want empty", p.SeekWorkPlaces)
	}

	// A set-but-DANGLING WorkStructureID (no matching structure in the snapshot) reads
	// as workless — the duty steer can't route there either, so seek-work still fires
	// rather than dead-zoning (LLM-168, raised in code review).
	dangling := worker(0)
	dangling.WorkStructureID = "ghost"
	if p := Build(mk(dangling), "lewis", nil); len(p.SeekWorkPlaces) != 1 || p.SeekWorkPlaces[0].Name != "Tavern" {
		t.Errorf("worker with dangling workplace: SeekWorkPlaces = %+v, want [Tavern]", p.SeekWorkPlaces)
	}

	// A NON-worker (no worker attribute) is not directed to seek work.
	if p := Build(mk(actorSnap(sim.StateIdle, "", 0, 0, "", 0)), "lewis", nil); len(p.SeekWorkPlaces) != 0 {
		t.Errorf("non-worker: SeekWorkPlaces = %+v, want empty", p.SeekWorkPlaces)
	}

	// A workless worker mid source-activity is NOT free to leave → no directory, so the
	// directive bit stays off and the owed-reply nag is preserved (LLM-160 review).
	busy := worker(0)
	busy.SourceActivityKind = sim.SourceActivityHarvest
	if p := Build(mk(busy), "lewis", nil); len(p.SeekWorkPlaces) != 0 {
		t.Errorf("workless worker mid source-activity: SeekWorkPlaces = %+v, want empty", p.SeekWorkPlaces)
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
