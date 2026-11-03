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
		perceptionScenario{
			name: "outdoors_near_a_warded_house",
			summary: "LLM-900: Sarah Walker outdoors among the houses. The Ellis House, four tiles off, has a horseshoe " +
				"over its door: '## Around you' carries 'A horseshoe is nailed over the door of the Ellis House.' Her " +
				"own Walker House is warded too and says nothing (the household want speaks for a home). The James " +
				"Farm has only a buried witch bottle (never seen), the Crane Cottage no ward, and the warded Thorne " +
				"Cottage is fifteen tiles off, out of sight (campSightTiles is 8) — none renders a line.",
			build: outdoorsNearAWardedHouse,
		},
	)
}

// outdoorsNearAWardedHouse: Sarah Walker outdoors at midday beside five houses;
// only the Ellis House is a neighbour's, in sight, with a horseshoe up.
func outdoorsNearAWardedHouse() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const sarahID = sim.ActorID("sarah")
	midday := 720
	at := func(t sim.TilePos) sim.WorldPos {
		return sim.WorldPos{X: float64(t.X-sim.PadX) * sim.TileSize, Y: float64(t.Y-sim.PadY) * sim.TileSize}
	}
	sarah := &sim.ActorSnapshot{
		Kind:            sim.KindNPCStateful,
		DisplayName:     "Sarah Walker",
		Role:            "weaver",
		State:           sim.StateIdle,
		Pos:             sim.TilePos{X: 100, Y: 130},
		HomeStructureID: "walker_house",
		Needs:           map[sim.NeedKey]int{},
		Inventory:       map[sim.ItemKind]int{},
	}
	snap := &sim.Snapshot{
		PublishedAt:      time.Date(2026, 10, 20, 16, 0, 0, 0, time.UTC),
		LocalMinuteOfDay: &midday,
		NeedThresholds:   sim.NeedThresholds{},
		Assets:           emptyAssetSet,
		Actors:           map[sim.ActorID]*sim.ActorSnapshot{sarahID: sarah},
		Structures:       map[sim.StructureID]*sim.Structure{},
		VillageObjects:   map[sim.VillageObjectID]*sim.VillageObject{},
	}
	house := func(id sim.StructureID, name string, tile sim.TilePos, tags ...string) {
		snap.Structures[id] = plainStructure(id, name)
		snap.VillageObjects[sim.VillageObjectID(id)] = &sim.VillageObject{
			ID: sim.VillageObjectID(id), DisplayName: name, Pos: at(tile), Tags: tags}
	}
	house("walker_house", "Walker House", sim.TilePos{X: 101, Y: 130}, sim.TagWardHorseshoe)
	house("ellis_house", "Ellis House", sim.TilePos{X: 104, Y: 130}, sim.TagWardHorseshoe, sim.TagWardWitchBottle)
	house("james_farm", "James Farm", sim.TilePos{X: 103, Y: 128}, sim.TagWardWitchBottle)
	house("crane_cottage", "Crane Cottage", sim.TilePos{X: 102, Y: 132})
	house("thorne_cottage", "Thorne Cottage", sim.TilePos{X: 115, Y: 130}, sim.TagWardHorseshoe)
	return snap, sarahID, nil
}

// TestGoldensHouseWardLineIffWardedHouseInSight — LLM-900: an outdoor actor is
// told of every house in sight with a horseshoe over the door (at most
// maxCampSightings), never their own home and never otherwise. Counted from the
// snapshot itself, with the same sight rule as the camp line, so it checks the
// build as well as the render. The vacuity floor is outdoors_near_a_warded_house.
func TestGoldensHouseWardLineIffWardedHouseInSight(t *testing.T) {
	sawLine := false
	for _, sc := range perceptionScenarios {
		snap, actorID, _ := sc.build()
		a := snap.Actors[actorID]
		want := 0
		if a != nil && a.InsideStructureID == "" {
			for _, o := range snap.VillageObjects {
				st := snap.Structures[sim.StructureID(o.ID)]
				if !sim.IsTent(o) && o.HasTag(sim.TagWardHorseshoe) && o.ID != sim.VillageObjectID(a.HomeStructureID) &&
					st != nil && st.DisplayName != "" && a.Pos.Chebyshev(o.Pos.Tile()) <= campSightTiles {
					want++
				}
			}
		}
		if want > maxCampSightings {
			want = maxCampSightings
		}
		prompt := renderScenario(sc)
		got := strings.Count(prompt, houseWardSightLead)
		if got != want {
			t.Errorf("%s: %d warded-house lines, want %d", sc.name, got, want)
		}
		if a != nil && a.HomeStructureID != "" {
			if home := snap.Structures[a.HomeStructureID]; home != nil && home.DisplayName != "" &&
				strings.Contains(prompt, houseWardSightProse(home.DisplayName)) {
				t.Errorf("%s: the actor is told of the horseshoe over their own door", sc.name)
			}
		}
		if got > 0 {
			sawLine = true
		}
	}
	if !sawLine {
		t.Error("no scenario renders a warded-house line — the invariant checked nothing")
	}
}

func TestBuildWardedHouseSightings(t *testing.T) {
	snap, id, _ := outdoorsNearAWardedHouse()
	a := snap.Actors[id]
	if got := buildWardedHouseSightings(snap, a); len(got) != 1 || got[0] != "Ellis House" {
		t.Fatalf("sightings = %v, want [Ellis House]", got)
	}

	// Two more warded houses in sight, both nearer: the cap keeps the nearest two.
	snap.VillageObjects["crane_cottage"].Tags = []string{sim.TagWardHorseshoe}
	snap.VillageObjects["james_farm"].Tags = []string{sim.TagWardHorseshoe}
	if got := buildWardedHouseSightings(snap, a); len(got) != 2 || got[0] != "Crane Cottage" || got[1] != "James Farm" {
		t.Errorf("capped sightings = %v, want [Crane Cottage James Farm]", got)
	}

	// A warded tent is the camp line's, not this one's.
	snap2, _, _ := outdoorsNearAWardedHouse()
	snap2.VillageObjects["ellis_house"].AssetID = sim.TentAssetID
	if got := buildWardedHouseSightings(snap2, snap2.Actors[id]); len(got) != 0 {
		t.Errorf("sightings with the house a tent = %v, want none", got)
	}

	// Indoors, nothing is seen.
	a.InsideStructureID = "walker_house"
	if got := buildWardedHouseSightings(snap, a); got != nil {
		t.Errorf("sightings indoors = %v, want nil", got)
	}

	if got, want := houseWardSightProse("Ellis Farm"), "A horseshoe is nailed over the door of the Ellis Farm."; got != want {
		t.Errorf("prose = %q, want %q", got, want)
	}
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
