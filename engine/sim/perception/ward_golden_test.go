package perception

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// ward_golden_test.go — LLM-897 golden scenarios, unit tests and cross-scenario
// invariants for a household's want for a ward after an omen.

func init() {
	perceptionScenarios = append(perceptionScenarios,
		perceptionScenario{
			name: "household_wants_a_ward_walking",
			summary: "LLM-897: two days ago the milk turned sour in John Ellis's pail. His wife Elizabeth, off-shift and " +
				"outdoors, reads '## A ward for the house': the household has talked of a horseshoe over the door, and " +
				"she could buy one at the Blacksmith (destination: blacksmith). Soft, no imperative: 'When it suits you'. " +
				"No engine text says what a horseshoe is for.",
			build: householdWantsAWardWalking,
		},
		perceptionScenario{
			name: "household_wants_a_ward_at_the_smith",
			summary: "LLM-897: the same Elizabeth in Ezekiel Crane's huddle at his forge. The section carries the shared " +
				"co-present buy-here imperative: pay_with_item with seller Ezekiel Crane, item horseshoe, qty up to 1.",
			build: householdWantsAWardAtTheSmith,
		},
		perceptionScenario{
			name: "household_ward_place_at_home",
			summary: "LLM-897: Elizabeth is home with the horseshoe she bought. The section turns into the place choice " +
				"('call place_ward with item \"horseshoe\"'), the one signal that advertises the place_ward tool. The buy " +
				"errand is gone: she holds the good.",
			build: householdWardPlaceAtHome,
		},
		perceptionScenario{
			name: "household_ward_already_up",
			summary: "LLM-897 negative: the horseshoe is already over the Ellis door, so the want is over (one ward is " +
				"enough) — no '## A ward for the house' section, though the omen is still inside the window.",
			build: householdWardAlreadyUp,
		},
	)
}

const (
	wardElizabethID = sim.ActorID("elizabeth")
	wardJohnID      = sim.ActorID("john")
	wardEzekielID   = sim.ActorID("ezekiel")
	wardHouse       = sim.StructureID("ellis_house")
)

// wardHouseholdSnapshot: the Ellis household two days after John's sour milk,
// Elizabeth unscheduled (so never on shift) at pos/inside, Ezekiel Crane the
// horseshoe-forging smith at his forge, sharing Elizabeth's huddle when huddle
// is set.
func wardHouseholdSnapshot(pos sim.WorldPos, inside sim.StructureID, huddle sim.HuddleID) *sim.Snapshot {
	now := 15 * 60
	published := time.Date(2026, 7, 14, 19, 0, 0, 0, time.UTC)
	zero := 0
	elizabeth := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "Elizabeth Ellis",
		Role:              "farmer",
		State:             sim.StateIdle,
		Pos:               pos.Tile(),
		InsideStructureID: inside,
		HomeStructureID:   wardHouse,
		CurrentHuddleID:   huddle,
		Coins:             20,
		Needs:             map[sim.NeedKey]int{},
		Inventory:         map[sim.ItemKind]int{},
		Acquaintances:     map[string]sim.Acquaintance{"Ezekiel Crane": {}, "John Ellis": {}},
	}
	john := &sim.ActorSnapshot{
		Kind:            sim.KindNPCStateful,
		DisplayName:     "John Ellis",
		Role:            "farmer",
		State:           sim.StateIdle,
		Pos:             sim.TilePos{X: 5, Y: 5},
		HomeStructureID: wardHouse,
		Needs:           map[sim.NeedKey]int{},
		Inventory:       map[sim.ItemKind]int{},
	}
	ezekiel := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "Ezekiel Crane",
		Role:              "blacksmith",
		State:             sim.StateIdle,
		Pos:               sim.WorldPos{X: 2000, Y: 2000}.Tile(),
		InsideStructureID: "blacksmith",
		WorkStructureID:   "blacksmith",
		CurrentHuddleID:   huddle,
		Needs:             map[sim.NeedKey]int{},
		Inventory:         map[sim.ItemKind]int{sim.HorseshoeKind: 2},
		RestockPolicy:     producePolicy(sim.HorseshoeKind, 2),
		Acquaintances:     map[string]sim.Acquaintance{"Elizabeth Ellis": {}},
	}
	snap := &sim.Snapshot{
		LocalMinuteOfDay: &now,
		PublishedAt:      published,
		NeedThresholds:   sim.NeedThresholds{},
		Assets:           emptyAssetSet,
		OmenWardWantDays: 7,
		Actors: map[sim.ActorID]*sim.ActorSnapshot{
			wardElizabethID: elizabeth, wardJohnID: john, wardEzekielID: ezekiel,
		},
		Structures: map[sim.StructureID]*sim.Structure{
			"blacksmith": plainStructure("blacksmith", "Blacksmith"),
			wardHouse:    plainStructure(wardHouse, "Ellis House"),
		},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			sim.VillageObjectID(wardHouse): {
				ID: sim.VillageObjectID(wardHouse), DisplayName: "Ellis House", Pos: sim.WorldPos{X: 100, Y: 100},
				LoiterOffsetX: &zero, LoiterOffsetY: &zero,
			},
		},
	}
	snap.Environment.Omens = sim.OmenRecord{
		LastAt:   map[sim.ActorID]time.Time{wardJohnID: published.Add(-48 * time.Hour)},
		LastKind: map[sim.ActorID]sim.OmenKind{wardJohnID: sim.OmenSourMilk},
	}
	return snap
}

func householdWantsAWardWalking() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	return wardHouseholdSnapshot(sim.WorldPos{X: 1000, Y: 1000}, "", ""), wardElizabethID, nil
}

func householdWantsAWardAtTheSmith() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	return wardHouseholdSnapshot(sim.WorldPos{X: 2000, Y: 2000}, "blacksmith", "smith_huddle"), wardElizabethID, nil
}

func householdWardPlaceAtHome() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	snap := wardHouseholdSnapshot(sim.WorldPos{X: 100, Y: 100}, wardHouse, "")
	snap.Actors[wardElizabethID].Inventory[sim.HorseshoeKind] = 1
	return snap, wardElizabethID, nil
}

func householdWardAlreadyUp() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	snap := wardHouseholdSnapshot(sim.WorldPos{X: 1000, Y: 1000}, "", "")
	house := snap.VillageObjects[sim.VillageObjectID(wardHouse)]
	house.Tags = append(house.Tags, sim.TagWardHorseshoe)
	return snap, wardElizabethID, nil
}

const wardSectionHeader = "## A ward for the house"

// TestGoldensPlaceWardIffPlaceChoice — the place_ward tool and its cue share one
// signal: the cue names place_ward exactly when the payload carries the place
// choice, and no other scenario mentions the tool. The vacuity floor is the
// household_ward_place_at_home scenario.
func TestGoldensPlaceWardIffPlaceChoice(t *testing.T) {
	sawPlace := false
	for _, sc := range perceptionScenarios {
		snap, actorID, warrants := sc.build()
		p := Build(snap, actorID, warrants)
		prompt := combinedPrompt(Render(p, DefaultRenderConfig()))
		names := strings.Contains(prompt, "place_ward")
		if (p.WardPlace != nil) != names {
			t.Errorf("scenario %q: WardPlace=%v but the prompt names place_ward=%v", sc.name, p.WardPlace != nil, names)
		}
		if p.WardPlace != nil {
			sawPlace = true
		}
	}
	if !sawPlace {
		t.Error("no scenario renders the place choice — the invariant checked nothing")
	}
}

// TestGoldensNoWardCueOnceWardedOrHeld — a household whose house carries a ward,
// or one of whose residents holds a horseshoe, is never sent out to buy one, and
// a ward section only ever renders for a resident whose house carries none.
func TestGoldensNoWardCueOnceWardedOrHeld(t *testing.T) {
	sawWant := false
	for _, sc := range perceptionScenarios {
		snap, actorID, warrants := sc.build()
		p := Build(snap, actorID, warrants)
		a := snap.Actors[actorID]
		prompt := combinedPrompt(Render(p, DefaultRenderConfig()))
		if !strings.Contains(prompt, wardSectionHeader) {
			continue
		}
		if a == nil || a.HomeStructureID == "" {
			t.Errorf("scenario %q: a ward section for an actor with no home", sc.name)
			continue
		}
		if sim.HomeWarded(snap.VillageObjects[sim.VillageObjectID(a.HomeStructureID)]) {
			t.Errorf("scenario %q: a ward section though the house carries a ward", sc.name)
		}
		if p.WardWant != nil {
			sawWant = true
			for id, m := range snap.Actors {
				if m.HomeStructureID == a.HomeStructureID && m.Inventory[sim.HorseshoeKind] > 0 {
					t.Errorf("scenario %q: the buy errand renders though %s holds a horseshoe", sc.name, id)
				}
			}
		}
	}
	if !sawWant {
		t.Error("no scenario renders the buy errand — the invariant checked nothing")
	}
}

func TestBuildHouseholdWardWant(t *testing.T) {
	snap, _, _ := householdWantsAWardWalking()
	a := snap.Actors[wardElizabethID]
	if got := buildHouseholdWardWant(snap, a); got == nil || got.Clause != "the milk turned sour in the pail" || len(got.Members) != 2 {
		t.Fatalf("want two days after John's omen = %+v, want the sour-milk clause and both Ellises", got)
	}

	// Outside the window: the want is over.
	snap.Environment.Omens.LastAt[wardJohnID] = snap.PublishedAt.Add(-7 * 24 * time.Hour)
	if got := buildHouseholdWardWant(snap, a); got != nil {
		t.Errorf("want seven days on = %+v, want nil", got)
	}

	// A wider window from the setting brings it back.
	snap.OmenWardWantDays = 10
	if got := buildHouseholdWardWant(snap, a); got == nil {
		t.Error("want inside a 10-day window = nil")
	}

	// An omen stamped before the kind was recorded: the want stands, plain line.
	delete(snap.Environment.Omens.LastKind, wardJohnID)
	if got := buildHouseholdWardWant(snap, a); got == nil || got.Clause != "" {
		t.Errorf("want with no recorded kind = %+v, want a plain-line want", got)
	}

	// The latest omen in the household gives the clause.
	snap.Environment.Omens.LastAt[wardElizabethID] = snap.PublishedAt.Add(-time.Hour)
	snap.Environment.Omens.LastKind[wardElizabethID] = sim.OmenColdHearth
	if got := buildHouseholdWardWant(snap, a); got == nil || got.Clause != "the hearth fire would not stay lit" {
		t.Errorf("clause = %+v, want the latest omen's", got)
	}

	// A neighbour's omen is not this household's.
	snap2, _, _ := householdWantsAWardWalking()
	snap2.Actors[wardJohnID].HomeStructureID = "other_house"
	if got := buildHouseholdWardWant(snap2, snap2.Actors[wardElizabethID]); got != nil {
		t.Errorf("want from a neighbour's omen = %+v, want nil", got)
	}

	// A visitor or a player wants nothing.
	snap3, _, _ := householdWantsAWardWalking()
	snap3.Actors[wardElizabethID].Kind = sim.KindPC
	if got := buildHouseholdWardWant(snap3, snap3.Actors[wardElizabethID]); got != nil {
		t.Errorf("want for a player = %+v, want nil", got)
	}
}

func TestBuildWardWant_ClearsForTheHousehold(t *testing.T) {
	cases := []struct {
		name  string
		edit  func(*sim.Snapshot)
		empty bool
	}{
		{name: "the baseline wants one", edit: func(*sim.Snapshot) {}},
		{name: "John holds a horseshoe", empty: true,
			edit: func(s *sim.Snapshot) { s.Actors[wardJohnID].Inventory[sim.HorseshoeKind] = 1 }},
		{name: "John has an offer standing for one", empty: true,
			edit: func(s *sim.Snapshot) {
				s.PayLedger = map[sim.LedgerID]*sim.PayLedgerEntry{1: {
					BuyerID: wardJohnID, SellerID: wardEzekielID, ItemKind: sim.HorseshoeKind,
					State: sim.PayLedgerStatePending, ExpiresAt: s.PublishedAt.Add(time.Minute)}}
			}},
		{name: "a lapsed offer does not count",
			edit: func(s *sim.Snapshot) {
				s.PayLedger = map[sim.LedgerID]*sim.PayLedgerEntry{1: {
					BuyerID: wardJohnID, SellerID: wardEzekielID, ItemKind: sim.HorseshoeKind,
					State: sim.PayLedgerStatePending, ExpiresAt: s.PublishedAt.Add(-time.Minute)}}
			}},
		{name: "asleep", empty: true,
			edit: func(s *sim.Snapshot) { s.Actors[wardElizabethID].State = sim.StateSleeping }},
		{name: "no seller anywhere", empty: true,
			edit: func(s *sim.Snapshot) { delete(s.Actors, wardEzekielID) }},
		{name: "on shift", empty: true,
			edit: func(s *sim.Snapshot) {
				start, end := 6*60, 18*60
				s.Actors[wardElizabethID].ScheduleStartMin = &start
				s.Actors[wardElizabethID].ScheduleEndMin = &end
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			snap, _, _ := householdWantsAWardWalking()
			tc.edit(snap)
			a := snap.Actors[wardElizabethID]
			got := buildWardWant(snap, wardElizabethID, a, buildHouseholdWardWant(snap, a))
			if (got == nil) != tc.empty {
				t.Errorf("buildWardWant = %+v, want nil=%v", got, tc.empty)
			}
		})
	}
}

func TestBuildWardPlaceChoice(t *testing.T) {
	snap, _, _ := householdWardPlaceAtHome()
	a := snap.Actors[wardElizabethID]
	want := buildHouseholdWardWant(snap, a)
	if got := buildWardPlaceChoice(a, want, snap); got == nil || len(got.Items) != 1 || got.Items[0] != sim.HorseshoeKind {
		t.Fatalf("place choice at home with a horseshoe = %+v", got)
	}
	a.MoveDestKind = sim.MoveDestinationKind("structure")
	if got := buildWardPlaceChoice(a, want, snap); got != nil {
		t.Errorf("place choice while walking = %+v, want nil", got)
	}
	a.MoveDestKind = ""
	a.InsideStructureID = ""
	if got := buildWardPlaceChoice(a, want, snap); got != nil {
		t.Errorf("place choice outside the house = %+v, want nil", got)
	}
	a.InsideStructureID = wardHouse
	if got := buildWardPlaceChoice(a, nil, snap); got != nil {
		t.Errorf("place choice with no household want = %+v, want nil", got)
	}
}
