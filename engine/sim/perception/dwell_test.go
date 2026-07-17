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
// supplied DwellCredits, plus optional structures and village objects for
// label resolution. loiterObjID is the actor's CurrentLoiterObjectID — the pin
// it is co-located with — which buildActiveDwellCredits gates each credit on
// (LLM-68); pass the credit's ObjectID to have it render, "" or a different id
// to exercise the walk-away gate. Used by the perception-dwell tests below.
func dwellSnap(credits map[sim.DwellCreditKey]*sim.DwellCredit, structures map[sim.StructureID]*sim.Structure, objects map[sim.VillageObjectID]*sim.VillageObject, loiterObjID sim.VillageObjectID) *sim.Snapshot {
	return &sim.Snapshot{
		Actors: map[sim.ActorID]*sim.ActorSnapshot{
			"hannah": {
				State: sim.StateEating,
				// All three needs unmet: an actor actively recovering at a pin has
				// an unmet need for whatever the credit eases. LLM-376 gates an
				// OBJECT dwell whose need is already at the floor out of the active-
				// dwell projection, so these fixtures must keep the eased need > 0
				// for the credit to render (which is what they exercise).
				Needs:                 map[sim.NeedKey]int{"hunger": 12, "thirst": 12, "tiredness": 12},
				DwellCredits:          credits,
				CurrentLoiterObjectID: loiterObjID,
			},
		},
		Structures:     structures,
		VillageObjects: objects,
		Scenes:         map[sim.SceneID]*sim.Scene{},
		Huddles:        map[sim.HuddleID]*sim.Huddle{},
	}
}

func TestBuildActorView_EmptyDwellCredits(t *testing.T) {
	snap := dwellSnap(nil, nil, nil, "")
	av := buildActorView(snap, "hannah", snap.Actors["hannah"])
	if av.ActiveDwellCredits != nil {
		t.Errorf("ActiveDwellCredits = %v, want nil for empty credits", av.ActiveDwellCredits)
	}
}

// TestBuildActorView_StaleCreditGatedByColocation is the LLM-68 regression:
// a DwellCredit lingers in the map between the actor's walk-away and the next
// dwell-tick sweep that deletes it. During that window perception must NOT keep
// rendering "you are resting at X" — that stale cue tells the actor to stay put
// and do nothing. buildActiveDwellCredits gates each credit on
// CurrentLoiterObjectID (the pin the actor is actually at), so a credit whose
// pin no longer matches drops immediately.
func TestBuildActorView_StaleCreditGatedByColocation(t *testing.T) {
	shadeOakCredit := func() map[sim.DwellCreditKey]*sim.DwellCredit {
		return map[sim.DwellCreditKey]*sim.DwellCredit{
			{ObjectID: "shade_oak", Attribute: "tiredness", Source: sim.DwellSourceObject}: {
				ObjectID:           "shade_oak",
				Attribute:          "tiredness",
				Source:             sim.DwellSourceObject,
				LastCreditedAt:     time.Now().UTC(),
				DwellDelta:         -2,
				DwellPeriodMinutes: 10,
			},
		}
	}
	objects := map[sim.VillageObjectID]*sim.VillageObject{
		"shade_oak": {ID: "shade_oak", AssetID: "tree-oak", DisplayName: "the old oak"},
	}

	// Walked off the pin entirely (the live Prudence case: idle inside her
	// residence, no pin owns her tile → resolver returned !ok → "").
	t.Run("walked away, at no pin", func(t *testing.T) {
		snap := dwellSnap(shadeOakCredit(), nil, objects, "")
		av := buildActorView(snap, "hannah", snap.Actors["hannah"])
		if av.ActiveDwellCredits != nil {
			t.Errorf("ActiveDwellCredits = %v, want nil (credit gated: actor at no pin)", av.ActiveDwellCredits)
		}
	})

	// Walked to a DIFFERENT pin — the stale shade-oak credit must still drop.
	t.Run("walked away, now at a different pin", func(t *testing.T) {
		snap := dwellSnap(shadeOakCredit(), nil, objects, "well")
		av := buildActorView(snap, "hannah", snap.Actors["hannah"])
		if av.ActiveDwellCredits != nil {
			t.Errorf("ActiveDwellCredits = %v, want nil (credit gated: actor at a different pin)", av.ActiveDwellCredits)
		}
	})

	// Still at the pin — the credit renders (the gate doesn't over-suppress a
	// live dwell, which would regress the meal/rest-parking cue WORK-409/411).
	t.Run("still at the pin", func(t *testing.T) {
		snap := dwellSnap(shadeOakCredit(), nil, objects, "shade_oak")
		av := buildActorView(snap, "hannah", snap.Actors["hannah"])
		if len(av.ActiveDwellCredits) != 1 {
			t.Fatalf("ActiveDwellCredits = %d, want 1 (credit live: actor still at pin)", len(av.ActiveDwellCredits))
		}
	})
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
	snap := dwellSnap(credits, structs, nil, "tavern")

	av := buildActorView(snap, "hannah", snap.Actors["hannah"])
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
	snap := dwellSnap(credits, nil, objects, "shade_oak")

	av := buildActorView(snap, "hannah", snap.Actors["hannah"])
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
		// Same pin as the two item credits — an actor is co-located with at
		// most one pin, so a multi-credit render is all-at-one-pin (e.g. ate
		// stew, drank ale, and is resting, all at the tavern). LLM-68.
		{ObjectID: "tavern", Attribute: "thirst", Source: sim.DwellSourceObject}: {
			ObjectID: "tavern", Attribute: "thirst", Source: sim.DwellSourceObject,
		},
	}
	snap := dwellSnap(credits, nil, nil, "tavern")
	av := buildActorView(snap, "hannah", snap.Actors["hannah"])

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
	snap := dwellSnap(credits, nil, nil, "tavern")
	av := buildActorView(snap, "hannah", snap.Actors["hannah"])
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
			name: "object dwell (tiredness): open-ended stay clause, no countdown/coins",
			c: DwellCreditView{
				Source: sim.DwellSourceObject, Attribute: "tiredness", StructureLabel: "the old oak",
			},
			want: "resting at the old oak, the longer you stay the more you recover, until you are rested. If you leave now you will stop recovering, and you will remain tired",
		},
		{
			name: "object dwell (thirst at a well): drink-source wording",
			c: DwellCreditView{
				Source: sim.DwellSourceObject, Attribute: "thirst", StructureLabel: "the well",
			},
			want: "drinking at the well, the longer you stay the more you recover, until your thirst is quenched. If you leave now you will stop recovering, and you will remain thirsty",
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
