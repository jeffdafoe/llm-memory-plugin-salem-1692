package perception

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// dwell_test.go — coverage of ActorView.ActiveDwellCredits projection
// in buildActorView + the perception-render of "currently:" lines.
// Snapshot fixture is hand-built so the test stays independent of
// LoadWorld / world goroutine plumbing.

// dwellSnap builds a minimal *sim.Snapshot with one actor carrying the
// supplied DwellCredits, plus optional structures and village objects
// for label resolution. Used by the perception-dwell tests below.
func dwellSnap(credits map[sim.DwellCreditKey]*sim.DwellCredit, structures map[sim.StructureID]*sim.Structure, objects map[sim.VillageObjectID]*sim.VillageObject) *sim.Snapshot {
	return &sim.Snapshot{
		Actors: map[sim.ActorID]*sim.ActorSnapshot{
			"hannah": {
				State:        sim.StateEating,
				Needs:        map[sim.NeedKey]int{"hunger": 12},
				DwellCredits: credits,
			},
		},
		Structures:     structures,
		VillageObjects: objects,
		Scenes:         map[sim.SceneID]*sim.Scene{},
		Huddles:        map[sim.HuddleID]*sim.Huddle{},
	}
}

func TestBuildActorView_EmptyDwellCredits(t *testing.T) {
	snap := dwellSnap(nil, nil, nil)
	av := buildActorView(snap, snap.Actors["hannah"])
	if av.ActiveDwellCredits != nil {
		t.Errorf("ActiveDwellCredits = %v, want nil for empty credits", av.ActiveDwellCredits)
	}
}

func TestBuildActorView_ItemCreditWithStructureLabel(t *testing.T) {
	remaining := 7
	credits := map[sim.DwellCreditKey]*sim.DwellCredit{
		{ObjectID: "tavern", Attribute: "hunger", Source: sim.DwellSourceItem}: {
			ObjectID:           "tavern",
			Kind:               "stew",
			Attribute:          "hunger",
			Source:             sim.DwellSourceItem,
			LastCreditedAt:     time.Now().UTC(),
			RemainingTicks:     &remaining,
			DwellDelta:         -1,
			DwellPeriodMinutes: 2,
		},
	}
	structs := map[sim.StructureID]*sim.Structure{
		"tavern": {ID: "tavern", DisplayName: "The Drunken Hare"},
	}
	snap := dwellSnap(credits, structs, nil)

	av := buildActorView(snap, snap.Actors["hannah"])
	if len(av.ActiveDwellCredits) != 1 {
		t.Fatalf("ActiveDwellCredits = %d, want 1", len(av.ActiveDwellCredits))
	}
	c := av.ActiveDwellCredits[0]
	if c.Kind != "stew" || c.Attribute != "hunger" {
		t.Errorf("Kind/Attribute = %q/%q, want stew/hunger", c.Kind, c.Attribute)
	}
	if c.StructureLabel != "The Drunken Hare" {
		t.Errorf("StructureLabel = %q, want 'The Drunken Hare'", c.StructureLabel)
	}
	if c.RemainingTicks == nil || *c.RemainingTicks != 7 {
		t.Errorf("RemainingTicks = %v, want 7", c.RemainingTicks)
	}
	if c.PeriodMinutes != 2 {
		t.Errorf("PeriodMinutes = %d, want 2", c.PeriodMinutes)
	}
}

func TestBuildActorView_ObjectCreditFallsBackToVillageObjectLabel(t *testing.T) {
	credits := map[sim.DwellCreditKey]*sim.DwellCredit{
		{ObjectID: "shade_oak", Attribute: "tiredness", Source: sim.DwellSourceObject}: {
			ObjectID:           "shade_oak",
			Attribute:          "tiredness",
			Source:             sim.DwellSourceObject,
			LastCreditedAt:     time.Now().UTC(),
			DwellDelta:         -2,
			DwellPeriodMinutes: 10,
		},
	}
	objects := map[sim.VillageObjectID]*sim.VillageObject{
		"shade_oak": {ID: "shade_oak", AssetID: "tree-oak", DisplayName: "the old oak"},
	}
	snap := dwellSnap(credits, nil, objects)

	av := buildActorView(snap, snap.Actors["hannah"])
	if len(av.ActiveDwellCredits) != 1 {
		t.Fatalf("ActiveDwellCredits = %d, want 1", len(av.ActiveDwellCredits))
	}
	c := av.ActiveDwellCredits[0]
	if c.StructureLabel != "the old oak" {
		t.Errorf("StructureLabel = %q, want 'the old oak' (village-object fallback)", c.StructureLabel)
	}
	if c.Source != sim.DwellSourceObject {
		t.Errorf("Source = %q, want object", c.Source)
	}
	if c.Kind != "" {
		t.Errorf("Kind = %q, want empty (object source)", c.Kind)
	}
	if c.RemainingTicks != nil {
		t.Errorf("RemainingTicks = %v, want nil (object source)", c.RemainingTicks)
	}
}

func TestBuildActorView_MultipleCreditsSortedDeterministically(t *testing.T) {
	rt := 5
	credits := map[sim.DwellCreditKey]*sim.DwellCredit{
		{ObjectID: "tavern", Attribute: "thirst", Source: sim.DwellSourceItem}: {
			ObjectID: "tavern", Kind: "ale", Attribute: "thirst",
			Source: sim.DwellSourceItem, RemainingTicks: &rt,
		},
		{ObjectID: "tavern", Attribute: "hunger", Source: sim.DwellSourceItem}: {
			ObjectID: "tavern", Kind: "stew", Attribute: "hunger",
			Source: sim.DwellSourceItem, RemainingTicks: &rt,
		},
		{ObjectID: "well", Attribute: "thirst", Source: sim.DwellSourceObject}: {
			ObjectID: "well", Attribute: "thirst", Source: sim.DwellSourceObject,
		},
	}
	snap := dwellSnap(credits, nil, nil)
	av := buildActorView(snap, snap.Actors["hannah"])

	if len(av.ActiveDwellCredits) != 3 {
		t.Fatalf("ActiveDwellCredits = %d, want 3", len(av.ActiveDwellCredits))
	}
	// Sort: (Source asc, Attribute asc, ObjectID asc). DwellSourceItem
	// = "item", DwellSourceObject = "object" — "i" < "o".
	wantOrder := []struct {
		source sim.DwellCreditSource
		attr   sim.NeedKey
	}{
		{sim.DwellSourceItem, "hunger"},
		{sim.DwellSourceItem, "thirst"},
		{sim.DwellSourceObject, "thirst"},
	}
	for i, w := range wantOrder {
		if av.ActiveDwellCredits[i].Source != w.source || av.ActiveDwellCredits[i].Attribute != w.attr {
			t.Errorf("[%d] = %+v, want source=%q attr=%q",
				i, av.ActiveDwellCredits[i], w.source, w.attr)
		}
	}
}

func TestBuildActorView_AliasIsolatedFromWorld(t *testing.T) {
	// Mutating the returned view's RemainingTicks must not race or
	// reach back into the snapshot. The buildActiveDwellCredits path
	// deep-copies the pointer.
	rt := 4
	credits := map[sim.DwellCreditKey]*sim.DwellCredit{
		{ObjectID: "tavern", Attribute: "hunger", Source: sim.DwellSourceItem}: {
			ObjectID: "tavern", Kind: "stew", Attribute: "hunger",
			Source: sim.DwellSourceItem, RemainingTicks: &rt,
		},
	}
	snap := dwellSnap(credits, nil, nil)
	av := buildActorView(snap, snap.Actors["hannah"])
	*av.ActiveDwellCredits[0].RemainingTicks = 99
	if rt != 4 {
		t.Errorf("snapshot RemainingTicks corrupted: %d", rt)
	}
}

// TestRenderActiveDwellCredit_StayMessage pins the ZBBS-WORK-409 dwell line —
// the persistent perception cue that keeps an NPC seated through a meal. It must
// name the time to FINISH and the stake of leaving (no "coins": an item dwell
// can be self-consumed pack food), stay food-agnostic via the need, and leave
// object dwells (no countdown) untouched.
func TestRenderActiveDwellCredit_StayMessage(t *testing.T) {
	rt := 6
	cases := []struct {
		name string
		c    DwellCreditView
		want string
	}{
		{
			name: "item meal: time-to-finish + stake, no coins",
			c: DwellCreditView{
				Source: sim.DwellSourceItem, Kind: "porridge", Attribute: "hunger",
				StructureLabel: "the Inn", RemainingTicks: &rt, PeriodMinutes: 2,
			},
			want: "eating porridge at the Inn, it will take you 12 more minute(s) to finish eating it all. If you leave now you will waste the rest, and you will remain hungry",
		},
		{
			name: "item drink: drink wording",
			c: DwellCreditView{
				Source: sim.DwellSourceItem, Kind: "ale", Attribute: "thirst",
				StructureLabel: "the Tavern", RemainingTicks: &rt, PeriodMinutes: 2,
			},
			want: "drinking ale at the Tavern, it will take you 12 more minute(s) to finish drinking it all. If you leave now you will waste the rest, and you will remain thirsty",
		},
		{
			name: "object dwell (no countdown): unchanged, no stay clause",
			c: DwellCreditView{
				Source: sim.DwellSourceObject, Attribute: "tiredness", StructureLabel: "the old oak",
			},
			want: "resting at the old oak",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := renderActiveDwellCredit(tc.c); got != tc.want {
				t.Errorf("renderActiveDwellCredit\n got:  %q\n want: %q", got, tc.want)
			}
		})
	}
}
