package perception

import (
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

func foragePolicy(item sim.ItemKind, cap int) *sim.RestockPolicy {
	return &sim.RestockPolicy{Restock: []sim.RestockEntry{
		{Item: item, Source: sim.RestockSourceForage, Max: cap},
	}}
}

// forageBush builds an owned forage-to-sell bush: a finite, gatherable,
// yield-only (Amount 0) refresh row for item with `avail` ripe units.
func forageBush(owner sim.ActorID, item sim.ItemKind, avail int) *sim.VillageObject {
	a := avail
	m := 10
	return &sim.VillageObject{
		OwnerActorID: owner,
		Refreshes: []*sim.ObjectRefresh{
			{Attribute: "hunger", Amount: 0, GatherItem: item, AvailableQuantity: &a, MaxQuantity: &m},
		},
	}
}

// remembersGather builds the KnownPlaces map marking each id as a remembered
// gather source for item — what LLM-77 ownership-seeding records for an owner's
// own bushes, and what buildForage now reads to source the section (LLM-79). An
// owner only sees a bush in "## Your bushes to harvest" if they remember it here.
func remembersGather(item sim.ItemKind, ids ...sim.VillageObjectID) map[sim.PlaceRef]*sim.KnownPlace {
	m := make(map[sim.PlaceRef]*sim.KnownPlace, len(ids))
	for _, id := range ids {
		m[sim.PlaceRef(id)] = &sim.KnownPlace{
			Ref:         sim.PlaceRef(id),
			Kind:        sim.PlaceKindObject,
			Affordances: []string{"gather:" + string(item)},
		}
	}
	return m
}

func TestBuildForage_NoPolicy_Nil(t *testing.T) {
	subj := &sim.ActorSnapshot{Inventory: map[sim.ItemKind]int{"raspberries": 0}}
	snap := &sim.Snapshot{
		Actors:            map[sim.ActorID]*sim.ActorSnapshot{"prudence": subj},
		RestockReorderPct: 25,
	}
	if v := buildForage(snap, "prudence", subj, false); v != nil {
		t.Fatalf("expected nil view with no RestockPolicy, got %+v", v)
	}
}

func TestBuildForage_DisabledPct_Nil(t *testing.T) {
	subj := &sim.ActorSnapshot{Inventory: map[sim.ItemKind]int{"raspberries": 0}, RestockPolicy: foragePolicy("raspberries", 10)}
	snap := &sim.Snapshot{
		Actors: map[sim.ActorID]*sim.ActorSnapshot{"prudence": subj},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			"bushA": forageBush("prudence", "raspberries", 10),
		},
		RestockReorderPct: 0, // feature disabled
	}
	if v := buildForage(snap, "prudence", subj, false); v != nil {
		t.Fatalf("expected nil view when RestockReorderPct==0, got %+v", v)
	}
}

func TestBuildForage_AboveThreshold_Nil(t *testing.T) {
	// 5 of 10 = 50%, above the 25% reorder threshold → no cue.
	subj := &sim.ActorSnapshot{Inventory: map[sim.ItemKind]int{"raspberries": 5}, RestockPolicy: foragePolicy("raspberries", 10)}
	snap := &sim.Snapshot{
		Actors: map[sim.ActorID]*sim.ActorSnapshot{"prudence": subj},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			"bushA": forageBush("prudence", "raspberries", 10),
		},
		RestockReorderPct: 25,
	}
	if v := buildForage(snap, "prudence", subj, false); v != nil {
		t.Fatalf("expected nil view above reorder threshold, got %+v", v)
	}
}

func TestBuildForage_LowStock_SurfacesOwnedBushes(t *testing.T) {
	// 2 of 10 = 20%, below 25% → low. Owns two raspberry bushes (4 + 10 ripe);
	// a third raspberry bush belongs to someone else and must be excluded. She
	// REMEMBERS all three (incl. the other's, e.g. gathered there once), so the
	// exclusion is the ownership liveness gate inside the remembered scan, not
	// just an absence-from-memory.
	subj := &sim.ActorSnapshot{Inventory: map[sim.ItemKind]int{"raspberries": 2}, RestockPolicy: foragePolicy("raspberries", 10),
		KnownPlaces: remembersGather("raspberries", "bushA", "bushB", "bushC")}
	snap := &sim.Snapshot{
		Actors: map[sim.ActorID]*sim.ActorSnapshot{"prudence": subj},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			"bushA": forageBush("prudence", "raspberries", 4),
			"bushB": forageBush("prudence", "raspberries", 10), // ripest → move handle
			"bushC": forageBush("other", "raspberries", 9),     // not hers
		},
		RestockReorderPct: 25,
	}
	v := buildForage(snap, "prudence", subj, false)
	if v == nil || len(v.Items) != 1 {
		t.Fatalf("expected one low item, got %+v", v)
	}
	it := v.Items[0]
	if it.CurrentQty != 2 || it.Cap != 10 {
		t.Errorf("on-hand/cap: got %d/%d want 2/10", it.CurrentQty, it.Cap)
	}
	if it.BushCount != 2 {
		t.Errorf("BushCount: got %d want 2 (the other's bush excluded)", it.BushCount)
	}
	if it.RipeUnits != 14 {
		t.Errorf("RipeUnits: got %d want 14", it.RipeUnits)
	}
	if it.MoveHandle != "bushB" {
		t.Errorf("MoveHandle: got %q want \"bushB\" (the ripest)", it.MoveHandle)
	}
}

// TestBuildForage_CustomerEngaged_Defers is the don't-abandon-a-customer guard
// (LLM-90): the harvest cue steers the grower to WALK OFF to her bushes, so while
// a sale is live at the stall (Build passes customerEngaged=true for a pending
// offer to her, a co-present customer, or a quote she has standing out) the whole
// section defers — she finishes the deal before stepping out. Same low-stock,
// ripe-bush setup as the surfacing test; only customerEngaged flips the result.
func TestBuildForage_CustomerEngaged_Defers(t *testing.T) {
	subj := &sim.ActorSnapshot{Inventory: map[sim.ItemKind]int{"raspberries": 2}, RestockPolicy: foragePolicy("raspberries", 10),
		KnownPlaces: remembersGather("raspberries", "bushA")}
	snap := &sim.Snapshot{
		Actors: map[sim.ActorID]*sim.ActorSnapshot{"prudence": subj},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			"bushA": forageBush("prudence", "raspberries", 10),
		},
		RestockReorderPct: 25,
	}
	// Without engagement the section surfaces (guards the test setup is otherwise live).
	if v := buildForage(snap, "prudence", subj, false); v == nil {
		t.Fatal("expected the section to surface when no customer is engaged")
	}
	if v := buildForage(snap, "prudence", subj, true); v != nil {
		t.Fatalf("expected nil view while a customer is engaged at the stall, got %+v", v)
	}
}

func TestBuildForage_LowStock_NoOwnedBushes_Nil(t *testing.T) {
	// Low on raspberries but owns no raspberry bushes (only a blueberry one) →
	// nothing to point at, so no cue for raspberries.
	subj := &sim.ActorSnapshot{Inventory: map[sim.ItemKind]int{"raspberries": 1}, RestockPolicy: foragePolicy("raspberries", 10)}
	snap := &sim.Snapshot{
		Actors: map[sim.ActorID]*sim.ActorSnapshot{"prudence": subj},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			"bushA": forageBush("prudence", "blueberries", 10),
		},
		RestockReorderPct: 25,
	}
	if v := buildForage(snap, "prudence", subj, false); v != nil {
		t.Fatalf("expected nil view when no owned bushes for the low item, got %+v", v)
	}
}

func TestBuildForage_NoneRipe_NoMoveHandle(t *testing.T) {
	// Owns bushes but all picked clean (0 ripe): still surface the section (she
	// knows it's low + she has a farm) but with no move handle.
	subj := &sim.ActorSnapshot{Inventory: map[sim.ItemKind]int{"raspberries": 0}, RestockPolicy: foragePolicy("raspberries", 10),
		KnownPlaces: remembersGather("raspberries", "bushA")}
	snap := &sim.Snapshot{
		Actors: map[sim.ActorID]*sim.ActorSnapshot{"prudence": subj},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			"bushA": forageBush("prudence", "raspberries", 0),
		},
		RestockReorderPct: 25,
	}
	v := buildForage(snap, "prudence", subj, false)
	if v == nil || len(v.Items) != 1 {
		t.Fatalf("expected one item, got %+v", v)
	}
	if v.Items[0].RipeUnits != 0 || v.Items[0].MoveHandle != "" {
		t.Errorf("expected 0 ripe + empty move handle, got %d / %q", v.Items[0].RipeUnits, v.Items[0].MoveHandle)
	}
}

func TestRenderForage_LowStock(t *testing.T) {
	v := &ForageView{Items: []ForageItemView{
		{ItemLabel: "raspberries", CurrentQty: 2, Cap: 10, BushCount: 2, RipeUnits: 14, MoveHandle: "bushB"},
	}}
	var b strings.Builder
	renderForage(&b, v)
	out := b.String()
	for _, want := range []string{
		"## Your bushes to harvest",
		"raspberries: 2 on hand of 10 cap (room for 8 more)",
		"You own 2 bush(es)",
		"14 ripe to pick now",
		`structure_id "bushB"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("render missing %q in:\n%s", want, out)
		}
	}
}

// TestBuild_ForageErrandWiring locks the LLM-90 composition that the parameter-
// level buildForage / buildDutySteer tests can't: Build must wire customerEngaged
// -> p.Forage -> DutySteer.ForageErrand. A future refactor of the Build wiring
// would slip past the unit tests but fail here. base() is Prudence on-shift at her
// own apothecary, berry shelf low (1 of 10), remembering her own still-owned
// raspberry bush — the actionable harvest setup; each subtest mutates it.
func TestBuild_ForageErrandWiring(t *testing.T) {
	base := func() (*sim.Snapshot, *sim.ActorSnapshot) {
		seller := &sim.ActorSnapshot{
			DisplayName:        "Prudence Ward",
			Kind:               sim.KindNPCStateful,
			BusinessownerState: &sim.BusinessownerState{},
			WorkStructureID:    "apothecary",
			InsideStructureID:  "apothecary",
			ScheduleStartMin:   dutyMinPtr(480),                        // 08:00
			ScheduleEndMin:     dutyMinPtr(1080),                       // 18:00
			Inventory:          map[sim.ItemKind]int{"raspberries": 1}, // 10% of 10 < 25%
			RestockPolicy:      &sim.RestockPolicy{Restock: []sim.RestockEntry{{Item: "raspberries", Source: sim.RestockSourceForage, Max: 10}}},
			KnownPlaces: map[sim.PlaceRef]*sim.KnownPlace{
				"bushA": {Ref: "bushA", Kind: sim.PlaceKindObject, Affordances: []string{"gather:raspberries"}},
			},
		}
		snap := &sim.Snapshot{
			Actors:     map[sim.ActorID]*sim.ActorSnapshot{"prudence": seller},
			Structures: map[sim.StructureID]*sim.Structure{"apothecary": {ID: "apothecary", DisplayName: "PW Apothecary"}},
			VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
				"bushA": {OwnerActorID: "prudence", Refreshes: []*sim.ObjectRefresh{
					{Amount: 0, GatherItem: "raspberries", AvailableQuantity: dutyMinPtr(10)},
				}},
			},
			RestockReorderPct: 25,
			LocalMinuteOfDay:  dutyMinPtr(600), // 10:00, within shift
		}
		return snap, seller
	}

	t.Run("no customer -> Forage set, at-post ForageErrand", func(t *testing.T) {
		snap, _ := base()
		p := Build(snap, "prudence", nil)
		if p.Forage == nil {
			t.Fatal("expected the forage cue (low shelf + remembered owned bush, no customer)")
		}
		if p.DutySteer == nil || !p.DutySteer.AtPost || !p.DutySteer.ForageErrand {
			t.Fatalf("expected at-post steer with ForageErrand, got %+v", p.DutySteer)
		}
	})

	t.Run("pending offer to seller -> Forage deferred, normal stabilizer", func(t *testing.T) {
		snap, _ := base()
		snap.PayLedger = map[sim.LedgerID]*sim.PayLedgerEntry{
			1: {ID: 1, BuyerID: "mary", SellerID: "prudence", State: sim.PayLedgerStatePending},
		}
		p := Build(snap, "prudence", nil)
		if p.Forage != nil {
			t.Fatal("expected forage deferred while a buyer's offer is pending")
		}
		if p.DutySteer == nil || !p.DutySteer.AtPost || p.DutySteer.ForageErrand {
			t.Fatalf("expected the normal at-post stabilizer (no ForageErrand), got %+v", p.DutySteer)
		}
	})

	t.Run("standing quote from seller -> Forage deferred", func(t *testing.T) {
		snap, _ := base()
		snap.Quotes = map[sim.QuoteID]*sim.SceneQuote{
			1: {ID: 1, SellerID: "prudence", TargetBuyer: "mary", ItemKind: "raspberries", State: sim.SceneQuoteStateActive},
		}
		p := Build(snap, "prudence", nil)
		if p.Forage != nil {
			t.Fatal("expected forage deferred while a quote she extended is still live")
		}
		if p.DutySteer == nil || p.DutySteer.ForageErrand {
			t.Fatalf("expected no ForageErrand while engaged, got %+v", p.DutySteer)
		}
	})

	t.Run("co-present customer in huddle at own post -> Forage deferred (broad guard)", func(t *testing.T) {
		snap, seller := base()
		seller.CurrentHuddleID = "h1"
		snap.Actors["mary"] = &sim.ActorSnapshot{DisplayName: "Goodwife Mary", Kind: sim.KindNPCStateful, CurrentHuddleID: "h1"}
		snap.Huddles = map[sim.HuddleID]*sim.Huddle{
			"h1": {ID: "h1", Members: map[sim.ActorID]struct{}{"prudence": {}, "mary": {}}},
		}
		p := Build(snap, "prudence", nil)
		if p.Forage != nil {
			t.Fatal("expected forage deferred while a companion shares her huddle at her post (broad abandon guard)")
		}
		if p.DutySteer == nil || p.DutySteer.ForageErrand {
			t.Fatalf("expected no ForageErrand while a customer is present, got %+v", p.DutySteer)
		}
	})
}

// TestRenderRestockWarrantLine_ForageRoutesToBushes: a forage-sourced restock
// warrant line points the grower at "## Your bushes to harvest", not the buy-side
// "## Restocking" section she has no entries in (LLM-90).
func TestRenderRestockWarrantLine_ForageRoutesToBushes(t *testing.T) {
	buy := renderRestockWarrantLine(1, "milk", sim.RestockSourceBuy)
	if !strings.Contains(buy, "see Restocking.") {
		t.Errorf("buy warrant line should point at Restocking, got %q", buy)
	}
	forage := renderRestockWarrantLine(2, "raspberries", sim.RestockSourceForage)
	if !strings.Contains(forage, "see Your bushes to harvest.") {
		t.Errorf("forage warrant line should point at the bushes, got %q", forage)
	}
	if strings.Contains(forage, "Restocking") {
		t.Errorf("forage warrant line must not mention Restocking, got %q", forage)
	}
}

func TestRenderForage_Nil_NoOutput(t *testing.T) {
	var b strings.Builder
	renderForage(&b, nil)
	if b.Len() != 0 {
		t.Fatalf("expected empty render for nil view, got %q", b.String())
	}
}

func TestBuildForage_MoveHandleTieLowestID(t *testing.T) {
	// Two owned bushes with equal positive stock: the move handle must be the
	// lower object id deterministically, regardless of map iteration order.
	subj := &sim.ActorSnapshot{Inventory: map[sim.ItemKind]int{"raspberries": 0}, RestockPolicy: foragePolicy("raspberries", 10),
		KnownPlaces: remembersGather("raspberries", "bushA", "bushB")}
	snap := &sim.Snapshot{
		Actors: map[sim.ActorID]*sim.ActorSnapshot{"prudence": subj},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			"bushB": forageBush("prudence", "raspberries", 5),
			"bushA": forageBush("prudence", "raspberries", 5),
		},
		RestockReorderPct: 25,
	}
	v := buildForage(snap, "prudence", subj, false)
	if v == nil || len(v.Items) != 1 {
		t.Fatalf("expected one item, got %+v", v)
	}
	if v.Items[0].MoveHandle != "bushA" {
		t.Fatalf("MoveHandle on equal stock: got %q want \"bushA\" (lowest id)", v.Items[0].MoveHandle)
	}
}

// TestBuildForage_OwnedButNotRemembered_Nil is the no-god-injection guarantee
// (LLM-79): the section is sourced from EARNED MEMORY, not an ownership world
// scan. An owner who owns a low-stock-triggering bush but has no memory of it
// (empty known-places) gets no section — the engine no longer injects the farm.
func TestBuildForage_OwnedButNotRemembered_Nil(t *testing.T) {
	subj := &sim.ActorSnapshot{Inventory: map[sim.ItemKind]int{"raspberries": 2}, RestockPolicy: foragePolicy("raspberries", 10)}
	// No KnownPlaces — she owns the bush but doesn't "remember" it.
	snap := &sim.Snapshot{
		Actors: map[sim.ActorID]*sim.ActorSnapshot{"prudence": subj},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			"bushA": forageBush("prudence", "raspberries", 10),
		},
		RestockReorderPct: 25,
	}
	if v := buildForage(snap, "prudence", subj, false); v != nil {
		t.Fatalf("an owned-but-unremembered bush must not surface (no god-injection), got %+v", v)
	}
}

// TestRenderForage_NoGatherMention_MoveToOnly pins the LLM-59/LLM-79 steering
// fix: the distant cue steers move_to ONLY and never names the `gather` tool
// (which isn't callable until the grower is adjacent — the at-bush proximity cue
// advertises it then). Naming it here drove the weak model to fixate on gather
// and skip the walk (the prod reject-retry loop).
func TestRenderForage_NoGatherMention_MoveToOnly(t *testing.T) {
	v := &ForageView{Items: []ForageItemView{
		{ItemLabel: "raspberries", CurrentQty: 2, Cap: 10, BushCount: 2, RipeUnits: 14, MoveHandle: "bushB"},
		{ItemLabel: "blueberries", CurrentQty: 0, Cap: 10, BushCount: 1, RipeUnits: 0, MoveHandle: ""}, // none-ripe arm
	}}
	var b strings.Builder
	renderForage(&b, v)
	out := b.String()
	if strings.Contains(out, "gather") {
		t.Errorf("forage cue must not name the gather tool (LLM-79 steering fix):\n%s", out)
	}
	if !strings.Contains(out, `Use move_to with structure_id "bushB" to walk out to them.`) {
		t.Errorf("forage cue must steer move_to:\n%s", out)
	}
}
