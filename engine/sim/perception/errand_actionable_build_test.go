package perception

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// errand_actionable_build_test.go — LLM-620, driven through Build rather than the
// predicates alone. TestRestockingViewActionable proves the predicate over
// hand-built views; it would NOT catch a caller reverting to `p.Restocking != nil`,
// nor a builder that produces a differently-shaped view from the same world state.
// These do (code_review).
//
// Each case asserts its PRECONDITION on the built view before asserting the steer,
// so a fixture that quietly stops producing the shape under test fails loudly
// instead of passing vacuously.

// conserveKeeperAwayFromPost builds a coin-poor keeper standing somewhere other than
// his own shop during working hours, low on a bought-in good with a live off-scene
// supplier, and sitting on unsold sellable stock — the LLM-294 conserve shape. The
// restock section then renders as a hold-off-buying steer with no imperative and no
// destination, so there is no errand to protect and shift duty must resume.
func conserveKeeperAwayFromPost(inside sim.StructureID, coins int) (*sim.Snapshot, sim.ActorID) {
	const (
		keeperID = sim.ActorID("josiah")
		farmerID = sim.ActorID("moses")
		store    = sim.StructureID("general_store")
		home     = sim.StructureID("thorne_residence")
		farm     = sim.StructureID("james_farm")
	)
	start, end := 360, 1260 // 06:00–21:00
	now := 1090             // 18:10 — on shift
	keeper := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "Josiah Thorne",
		Role:              "shopkeeper",
		State:             sim.StateIdle,
		Pos:               sim.TilePos{X: 87, Y: 144},
		WorkStructureID:   store,
		HomeStructureID:   home,
		InsideStructureID: inside,
		ScheduleStartMin:  &start,
		ScheduleEndMin:    &end,
		Coins:             coins,
		Needs:             map[sim.NeedKey]int{},
		// wheat low (the restock want), firewood heavily overstocked (the unsold
		// sellable stock conserve needs to name in its sell nudge).
		Inventory: map[sim.ItemKind]int{"wheat": 0, "firewood": 30},
		RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: "wheat", Source: sim.RestockSourceBuy, Max: 20},
			{Item: "firewood", Source: sim.RestockSourceBuy, Max: 8},
		}},
	}
	farmer := &sim.ActorSnapshot{
		Kind:            sim.KindNPCShared,
		DisplayName:     "Moses James",
		State:           sim.StateIdle,
		Pos:             sim.TilePos{X: 20, Y: 100},
		WorkStructureID: farm,
		Inventory:       map[sim.ItemKind]int{"wheat": 40},
		RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: "wheat", Source: sim.RestockSourceProduce, Max: 40},
		}},
	}
	snap := &sim.Snapshot{
		PublishedAt:      time.Date(2026, 7, 1, 18, 10, 0, 0, time.UTC),
		LocalMinuteOfDay: &now,
		NeedThresholds:   sim.NeedThresholds{},
		Assets:           emptyAssetSet,
		Actors:           map[sim.ActorID]*sim.ActorSnapshot{keeperID: keeper, farmerID: farmer},
		Structures: map[sim.StructureID]*sim.Structure{
			store: plainStructure(store, "General Store"),
			home:  plainStructure(home, "Thorne Residence"),
			farm:  plainStructure(farm, "James Farm"),
		},
		ItemKinds: map[sim.ItemKind]*sim.ItemKindDef{
			"wheat":    {Name: "wheat", Capabilities: []string{"portable"}, DisplayLabel: "Wheat", Category: sim.ItemCategoryMaterial},
			"firewood": {Name: "firewood", Capabilities: []string{"portable"}, DisplayLabel: "Firewood", Category: sim.ItemCategoryMaterial},
		},
		Recipes: map[sim.ItemKind]*sim.ItemRecipe{
			"wheat":    {OutputItem: "wheat", OutputQty: 1, RateQty: 1, RatePerHours: 1, WholesalePrice: 1, RetailPrice: 1},
			"firewood": {OutputItem: "firewood", OutputQty: 1, RateQty: 1, RatePerHours: 1, WholesalePrice: 1, RetailPrice: 2},
		},
		RestockReorderPct: 25,
	}
	return snap, keeperID
}

func TestBuild_ConserveRestockIsNotAnErrand(t *testing.T) {
	const (
		store = sim.StructureID("general_store")
		home  = sim.StructureID("thorne_residence")
	)
	// Purse healthy → an ordinary actionable restock errand. The control: this is the
	// case the HOME-400 suppression exists for, and it must keep working.
	t.Run("healthy purse away from post -> errand holds the yank off", func(t *testing.T) {
		snap, id := conserveKeeperAwayFromPost(home, 114)
		p := Build(snap, id, nil)
		if p.Restocking == nil || p.Restocking.Conserve {
			t.Fatalf("precondition: want an ordinary (non-conserve) restock section, got %+v", p.Restocking)
		}
		if !p.Restocking.Actionable() {
			t.Fatalf("precondition: want an actionable restock section, got %+v", p.Restocking)
		}
		wantNoYank(t, p.DutySteer, "an actionable restock errand away from post")
	})
	// Coin-poor and overstocked → conserve. Render emits a hold-off-buying steer with
	// no destination, so nothing is in progress and duty resumes.
	t.Run("conserving away from post -> the yank resumes", func(t *testing.T) {
		snap, id := conserveKeeperAwayFromPost(home, 1)
		snap.MerchantCoinFloor = 10
		p := Build(snap, id, nil)
		if p.Restocking == nil || !p.Restocking.Conserve {
			t.Fatalf("precondition: want the conserve restock section, got %+v", p.Restocking)
		}
		if p.Restocking.Actionable() {
			t.Fatal("a hold-off-buying steer must not read as an errand in progress")
		}
		if p.DutySteer == nil || !p.DutySteer.ToWork {
			t.Fatalf("want the to-work steer restored for a conserving keeper away from his post, got %+v", p.DutySteer)
		}
	})
	// Same conserve state, but standing AT the post: the stabilizer must be the plain
	// "stay and look after your work" form, not the LLM-491 step-out permission — the
	// other end of the same unpinning.
	//
	// NOTE this arm passes both before and after LLM-620, so it is NOT evidence for
	// this change: SupplyErrand is derived at the end of Build from HasWalkToSupplier,
	// which already returned false under Conserve. It is here as a regression guard on
	// the pairing — the at-post end must keep agreeing with the away-from-post end,
	// and nothing else asserts that for the conserve case.
	t.Run("conserving at post -> the plain stabilizer, not a step-out", func(t *testing.T) {
		snap, id := conserveKeeperAwayFromPost(store, 1)
		snap.MerchantCoinFloor = 10
		p := Build(snap, id, nil)
		if p.Restocking == nil || !p.Restocking.Conserve {
			t.Fatalf("precondition: want the conserve restock section, got %+v", p.Restocking)
		}
		if p.DutySteer == nil || !p.DutySteer.AtPost {
			t.Fatalf("precondition: want the at-post stabilizer, got %+v", p.DutySteer)
		}
		if p.DutySteer.SupplyErrand {
			t.Error("a conserving keeper at his post must not be given the step-out permission — the cue beside it is telling him NOT to buy")
		}
	})
}

// TestBuild_AllBlockedRestockIsNotAnErrand: every supplier of the low item is one
// the keeper cannot transact with, so the section renders reasons with deliberately
// no destination ids (LLM-406). There is nowhere to go, so shift duty resumes.
func TestBuild_AllBlockedRestockIsNotAnErrand(t *testing.T) {
	const home = sim.StructureID("thorne_residence")
	snap, id := conserveKeeperAwayFromPost(home, 114)
	// Drop the overstocked ware so firewood cannot supply a second, unblocked item.
	keeper := snap.Actors[id]
	keeper.Inventory = map[sim.ItemKind]int{"wheat": 0}
	keeper.RestockPolicy = &sim.RestockPolicy{Restock: []sim.RestockEntry{
		{Item: "wheat", Source: sim.RestockSourceBuy, Max: 20},
	}}
	// He remembers the only wheat supplier shut, which is what turns its vendor entry
	// into a Blocked one rather than a walk-to bullet.
	keeper.Observed = sim.NewObservedStates(map[sim.ObservedStateKey]time.Time{
		{StructureID: "james_farm", Condition: sim.ObservedClosed}: snap.PublishedAt.Add(-time.Hour),
	})
	p := Build(snap, id, nil)
	if p.Restocking == nil || !p.Restocking.AllBlocked() {
		t.Fatalf("precondition: want an all-blocked restock section, got %+v", p.Restocking)
	}
	if p.Restocking.Actionable() {
		t.Fatal("an item nobody will sell him is a want with no step to take, not an errand")
	}
	if p.DutySteer == nil || !p.DutySteer.ToWork {
		t.Fatalf("want the to-work steer restored when every supplier is blocked, got %+v", p.DutySteer)
	}
}

// TestBuild_GrowerOnHisOnlyRipeBushKeepsTheSuppression pins the ordering
// ForageView.Actionable depends on: buildForage accumulates RipeUnits BEFORE it
// skips the bush the grower already stands on, so the LLM-617 nowhere-to-walk case
// still reads as an errand. If that ordering ever changed, a grower standing on his
// only ripe bush would be yanked off it mid-harvest — the exact wedge LLM-617 fixed
// — and no golden covers it, because the LLM-617 scenarios carry no work anchor and
// so build no duty steer at all (code_review).
func TestBuild_GrowerOnHisOnlyRipeBushKeepsTheSuppression(t *testing.T) {
	const (
		mosesID = sim.ActorID("moses")
		farm    = sim.StructureID("james_farm")
		home    = sim.StructureID("james_residence")
	)
	zero := 0
	start, end := 360, 1080
	now := 600 // 10:00 — on shift
	bushTile := sim.WorldPos{X: 10 * 32, Y: 87 * 32}
	moses := &sim.ActorSnapshot{
		Kind:              sim.KindNPCShared,
		DisplayName:       "Moses James",
		Role:              "farmer",
		State:             sim.StateIdle,
		Pos:               bushTile.Tile(), // standing ON the bush's loiter pin
		WorkStructureID:   farm,
		HomeStructureID:   home,
		InsideStructureID: "", // outdoors at the field, away from his post
		ScheduleStartMin:  &start,
		ScheduleEndMin:    &end,
		Needs:             map[sim.NeedKey]int{},
		Inventory:         map[sim.ItemKind]int{"wheat": 0},
		RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: "wheat", Source: sim.RestockSourceForage, Max: 30},
		}},
		KnownPlaces: map[sim.PlaceRef]*sim.KnownPlace{
			"only_bush": {Ref: "only_bush", Kind: sim.PlaceKindObject, Affordances: []string{"gather:wheat"}},
		},
	}
	snap := &sim.Snapshot{
		LocalMinuteOfDay:  &now,
		NeedThresholds:    sim.NeedThresholds{},
		Assets:            emptyAssetSet,
		RestockReorderPct: 25,
		Actors:            map[sim.ActorID]*sim.ActorSnapshot{mosesID: moses},
		Structures: map[sim.StructureID]*sim.Structure{
			farm: plainStructure(farm, "James Farm"),
			home: plainStructure(home, "James Residence"),
		},
		ItemKinds: map[sim.ItemKind]*sim.ItemKindDef{
			"wheat": {Name: "wheat", Capabilities: []string{"portable"}, DisplayLabel: "Wheat", Category: sim.ItemCategoryMaterial},
		},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			"only_bush": {
				ID:            "only_bush",
				DisplayName:   "Wheat",
				Pos:           bushTile,
				OwnerActorID:  mosesID,
				LoiterOffsetX: &zero,
				LoiterOffsetY: &zero,
				Refreshes: []*sim.ObjectRefresh{
					{Amount: 0, GatherItem: "wheat", AvailableQuantity: intp(9), MaxQuantity: intp(9)},
				},
			},
		},
	}
	p := Build(snap, mosesID, nil)
	if p.Forage == nil || len(p.Forage.Items) != 1 {
		t.Fatalf("precondition: want the owned-bush forage cue, got %+v", p.Forage)
	}
	if it := p.Forage.Items[0]; !it.AtRipeBush || it.MoveHandle != "" {
		t.Fatalf("precondition: want the LLM-617 shape (standing at the only ripe bush, no walk handle), got %+v", it)
	}
	if !p.Forage.Actionable() {
		t.Fatal("standing on the only ripe bush is an errand in progress — gather is callable right here")
	}
	wantNoYank(t, p.DutySteer, "standing on his only ripe bush, mid-harvest")
}
