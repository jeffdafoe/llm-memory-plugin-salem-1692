package perception

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// deadEndGoldenBase builds the LLM-154 at-location dead-end fixture: a laborer
// (Goodman Silence) at the Tavern, whose only keeper (John Ellis) works there.
// The caller places the customer (inside the interior, or outdoors at the loiter
// slot) and sets the keeper's state, so one fixture drives the shut/open ×
// inside/loitering matrix. An asleep keeper reads the business shut (LLM-126:
// the innkeeper sleeps AT the inn); an awake one tends it. Fixed PublishedAt, no
// orders → byte-stable.
func deadEndGoldenBase(customerInside bool, keeperState sim.ActorState) (*sim.Snapshot, sim.ActorID) {
	const (
		customerID = sim.ActorID("silence")
		keeperID   = sim.ActorID("john_ellis")
		tavern     = sim.StructureID("tavern")
	)
	zero := 0
	now := 13 * 60 // 13:00 — daytime, so no sleep/return-to-post cue competes for the prompt
	published := time.Date(2026, 6, 25, 13, 0, 0, 0, time.UTC)
	tavernPin := sim.WorldPos{X: 120, Y: 120}
	customer := &sim.ActorSnapshot{
		Kind:        sim.KindNPCStateful,
		DisplayName: "Goodman Silence",
		Role:        "laborer",
		State:       sim.StateIdle,
		Coins:       6,
		Needs:       map[sim.NeedKey]int{},
	}
	if customerInside {
		customer.InsideStructureID = tavern
		// Standing in the interior with the keeper asleep there: he is co-present
		// but not addressable — name him so the room doesn't read empty.
		if keeperState == sim.StateSleeping {
			customer.ColocatedSleeperIDs = []sim.ActorID{keeperID}
		}
	} else {
		customer.Pos = tavernPin.Tile() // outdoors, at the Tavern's loiter pin (offset 0)
	}
	keeper := &sim.ActorSnapshot{
		Kind:              sim.KindNPCShared,
		DisplayName:       "John Ellis",
		Role:              "innkeeper",
		State:             keeperState,
		WorkStructureID:   tavern,
		InsideStructureID: tavern,
		Coins:             20,
		Needs:             map[sim.NeedKey]int{},
	}
	nm := now
	snap := &sim.Snapshot{
		PublishedAt:      published,
		LocalMinuteOfDay: &nm,
		NeedThresholds:   sim.NeedThresholds{},
		Assets:           emptyAssetSet,
		Actors:           map[sim.ActorID]*sim.ActorSnapshot{customerID: customer, keeperID: keeper},
		Structures: map[sim.StructureID]*sim.Structure{
			tavern: innStructure(tavern, "Tavern"),
		},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			sim.VillageObjectID(tavern): {
				ID:            sim.VillageObjectID(tavern),
				DisplayName:   "Tavern",
				Pos:           tavernPin,
				LoiterOffsetX: &zero,
				LoiterOffsetY: &zero,
			},
		},
	}
	return snap, customerID
}

func customerAtShutBusinessLoitering() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	snap, id := deadEndGoldenBase(false, sim.StateSleeping)
	return snap, id, nil
}

func customerAtShutBusinessInside() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	snap, id := deadEndGoldenBase(true, sim.StateSleeping)
	return snap, id, nil
}

func customerAtOpenBusiness() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	snap, id := deadEndGoldenBase(false, sim.StateIdle)
	return snap, id, nil
}

// TestShutBusinessCueOnlyWhenUntended is the LLM-154 cross-scenario invariant: the
// at-location "is shut — no one is tending it" clause appears in EXACTLY the scenarios
// where the actor stands at a business no awake keeper is tending, and never at an
// attended one. The live, situated complement of the ObservedClosed memory invariant
// (TestExperientialShutCueOnlyWhenRemembered). The matrix must exercise both branches
// for the check to mean anything.
func TestShutBusinessCueOnlyWhenUntended(t *testing.T) {
	const marker = "is shut — no one is tending it"
	untended := map[string]bool{
		"customer_at_shut_business_loitering": true,
		"customer_at_shut_business_inside":    true,
	}
	var sawUntended, sawTended bool
	for _, sc := range perceptionScenarios {
		sc := sc
		got := renderScenario(sc)
		want := untended[sc.name]
		if want {
			sawUntended = true
		} else {
			sawTended = true
		}
		if has := strings.Contains(got, marker); has != want {
			t.Errorf("scenario %q: shut cue present=%v, want %v", sc.name, has, want)
		}
	}
	if !sawUntended || !sawTended {
		t.Errorf("matrix must exercise both branches: sawUntended=%v sawTended=%v", sawUntended, sawTended)
	}
}

// TestIsShutBusiness pins the live dead-end gate (LLM-154): a business reads shut
// only when it has a worker and no awake worker is tending it, the actor's own
// workplace is never read as shut, and a non-business is never shut. Mirrors the
// sim-side keeperPresentAt gates (StateSleeping disqualifies, StateResting still
// counts, a wandered keeper isn't present — closed_business.go).
func TestIsShutBusiness(t *testing.T) {
	const (
		tavern = sim.StructureID("tavern")
		home   = sim.StructureID("cottage")
	)
	zero := 0
	villageObjects := map[sim.VillageObjectID]*sim.VillageObject{
		sim.VillageObjectID(tavern): {
			ID: sim.VillageObjectID(tavern), DisplayName: "Tavern",
			Pos: sim.WorldPos{X: 50, Y: 50}, LoiterOffsetX: &zero, LoiterOffsetY: &zero,
		},
	}
	mkSnap := func(workers ...*sim.ActorSnapshot) *sim.Snapshot {
		actors := map[sim.ActorID]*sim.ActorSnapshot{}
		for i, w := range workers {
			actors[sim.ActorID(fmt.Sprintf("w%d", i))] = w
		}
		return &sim.Snapshot{
			Actors:         actors,
			Structures:     map[sim.StructureID]*sim.Structure{tavern: innStructure(tavern, "Tavern"), home: plainStructure(home, "Cottage")},
			VillageObjects: villageObjects,
		}
	}
	keeperInside := func(state sim.ActorState) *sim.ActorSnapshot {
		return &sim.ActorSnapshot{WorkStructureID: tavern, InsideStructureID: tavern, State: state}
	}
	customer := &sim.ActorSnapshot{} // a passer-by who works nowhere

	cases := []struct {
		name string
		snap *sim.Snapshot
		a    *sim.ActorSnapshot
		stID sim.StructureID
		want bool
	}{
		{"keeper asleep inside → shut", mkSnap(keeperInside(sim.StateSleeping)), customer, tavern, true},
		{"keeper awake inside → open", mkSnap(keeperInside(sim.StateIdle)), customer, tavern, false},
		{"keeper on break (resting) → open", mkSnap(keeperInside(sim.StateResting)), customer, tavern, false},
		{"keeper awake but wandered off → shut", mkSnap(&sim.ActorSnapshot{WorkStructureID: tavern, State: sim.StateIdle, Pos: sim.WorldPos{X: 9000, Y: 9000}.Tile()}), customer, tavern, true},
		{"no workers (a residence) → not a business", mkSnap(), customer, tavern, false},
		{"actor's own workplace is never shut", mkSnap(keeperInside(sim.StateSleeping)), &sim.ActorSnapshot{WorkStructureID: tavern}, tavern, false},
		{"empty structure id", mkSnap(keeperInside(sim.StateSleeping)), customer, "", false},
		{"nil actor", mkSnap(keeperInside(sim.StateSleeping)), nil, tavern, false},
	}
	for _, c := range cases {
		if got := isShutBusiness(c.snap, c.a, c.stID); got != c.want {
			t.Errorf("%s: isShutBusiness = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestDeadEndClause pins the rendered sentence (LLM-154): named from the inside or
// outdoors structure field, sentence-start capitalized, with the possessive
// proper-name path leaving its article-free name intact. Empty when there is no
// dead end or no place name.
func TestDeadEndClause(t *testing.T) {
	cases := []struct {
		name string
		view SurroundingsView
		want string
	}{
		{"no dead end", SurroundingsView{}, ""},
		{"inside, common-noun place", SurroundingsView{LocationDeadEnd: DeadEndShutBusiness, StructureName: "Tavern"}, "The Tavern is shut — no one is tending it."},
		{"outdoors, common-noun place", SurroundingsView{LocationDeadEnd: DeadEndShutBusiness, NearbyStructureName: "Tavern"}, "The Tavern is shut — no one is tending it."},
		{"possessive proper name keeps no article", SurroundingsView{LocationDeadEnd: DeadEndShutBusiness, StructureName: "Hannah's Inn"}, "Hannah's Inn is shut — no one is tending it."},
		{"dead end but no place name → empty", SurroundingsView{LocationDeadEnd: DeadEndShutBusiness}, ""},
	}
	for _, c := range cases {
		if got := deadEndClause(c.view); got != c.want {
			t.Errorf("%s: deadEndClause = %q, want %q", c.name, got, c.want)
		}
	}
}
