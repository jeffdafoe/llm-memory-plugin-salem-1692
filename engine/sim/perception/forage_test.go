package perception

import (
	"strings"
	"testing"
	"time"

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
		`destination "bushB"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("render missing %q in:\n%s", want, out)
		}
	}
}

// TestForageView_ActionableErrand covers the LLM-622 classifier directly. The
// own-before-free precedence is behaviour now, not an implementation detail — it
// decides whether the at-post reframe may claim the target as the subject's own —
// so the both-ripe row is the load-bearing one (code_review). RipeUnits is what
// counts, not the presence of an entry: the cue renders on LOW STOCK, so a view
// listing bushes with nothing ripe is a want with no step to take (LLM-620).
func TestForageView_ActionableErrand(t *testing.T) {
	ripeOwn := ForageItemView{ItemLabel: "raspberries", RipeUnits: 3}
	bareOwn := ForageItemView{ItemLabel: "raspberries", RipeUnits: 0}
	ripeWild := WildForageItemView{ItemLabel: "water", RipeUnits: 20}
	bareWild := WildForageItemView{ItemLabel: "water", RipeUnits: 0}

	for _, tc := range []struct {
		name string
		view *ForageView
		want ForageErrandKind
	}{
		{"nil view", nil, ForageErrandNone},
		{"empty view", &ForageView{}, ForageErrandNone},
		{"own bushes, none ripe", &ForageView{Items: []ForageItemView{bareOwn}}, ForageErrandNone},
		{"free source, none ripe", &ForageView{WildSources: []WildForageItemView{bareWild}}, ForageErrandNone},
		{"neither ripe", &ForageView{Items: []ForageItemView{bareOwn}, WildSources: []WildForageItemView{bareWild}}, ForageErrandNone},
		{"own ripe only", &ForageView{Items: []ForageItemView{ripeOwn}}, ForageErrandOwnBushes},
		{"free ripe only", &ForageView{WildSources: []WildForageItemView{ripeWild}}, ForageErrandFreeSources},
		{"both ripe -> own wins", &ForageView{Items: []ForageItemView{ripeOwn}, WildSources: []WildForageItemView{ripeWild}}, ForageErrandOwnBushes},
		{"own bare, free ripe -> free", &ForageView{Items: []ForageItemView{bareOwn}, WildSources: []WildForageItemView{ripeWild}}, ForageErrandFreeSources},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.view.ActionableErrand(); got != tc.want {
				t.Errorf("ActionableErrand() = %d, want %d", got, tc.want)
			}
			// Actionable delegates, so the two can never disagree about ripeness.
			if got, want := tc.view.Actionable(), tc.want != ForageErrandNone; got != want {
				t.Errorf("Actionable() = %v, want %v", got, want)
			}
		})
	}
}

// TestForageErrandKind_Errand pins the closed whitelist: a kind render has no arm
// for must not read as an errand, or it would suppress the to-work yank while
// producing no at-post reframe (LLM-622, code_review).
func TestForageErrandKind_Errand(t *testing.T) {
	for kind, want := range map[ForageErrandKind]bool{
		ForageErrandNone:        false,
		ForageErrandOwnBushes:   true,
		ForageErrandFreeSources: true,
		ForageErrandKind(200):   false,
	} {
		if got := kind.Errand(); got != want {
			t.Errorf("ForageErrandKind(%d).Errand() = %v, want %v", kind, got, want)
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
	// WorkerWorkingOffer skips an offer with a nil WorkingUntil, so a laboring
	// fixture needs the deadline set or the peer never reads as mid-job.
	laborUntil := time.Date(1692, 6, 1, 15, 0, 0, 0, time.UTC)
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
		if p.DutySteer == nil || !p.DutySteer.AtPost || p.DutySteer.ForageErrand != ForageErrandOwnBushes {
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
		if p.DutySteer == nil || !p.DutySteer.AtPost || p.DutySteer.ForageErrand != ForageErrandNone {
			t.Fatalf("expected the normal at-post stabilizer (no ForageErrand), got %+v", p.DutySteer)
		}
	})

	t.Run("standing quote from seller -> Forage deferred", func(t *testing.T) {
		snap, _ := base()
		snap.Quotes = map[sim.QuoteID]*sim.SceneQuote{
			1: {ID: 1, SellerID: "prudence", TargetBuyer: "mary", Lines: []sim.QuoteLine{{ItemKind: "raspberries", Qty: 1}}, State: sim.SceneQuoteStateActive},
		}
		p := Build(snap, "prudence", nil)
		if p.Forage != nil {
			t.Fatal("expected forage deferred while a quote she extended is still live")
		}
		if p.DutySteer == nil || p.DutySteer.ForageErrand != ForageErrandNone {
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
		if p.DutySteer == nil || p.DutySteer.ForageErrand != ForageErrandNone {
			t.Fatalf("expected no ForageErrand while a customer is present, got %+v", p.DutySteer)
		}
	})

	// The subject's own hired worker is NOT custom. LLM-231 settled this for the
	// sale-target question (buildOfferableCustomers drops a laboring peer even for
	// their own employer); the customerEngaged co-presence arm never asked, so an
	// employer standing in his own shop beside a worker he hired read as mid-sale
	// for the whole contract — hours, not the transient encounter the deferral is
	// built for. Live: Joseph Scott, the only actor permitted to gather water,
	// hired a hauler 11:13-15:13 and lost his water cue on the first tick after the
	// work began, while the same prompt said "Lewis Walker is working a job for you".
	t.Run("own hired worker mid-job at own post -> Forage survives", func(t *testing.T) {
		snap, seller := base()
		seller.CurrentHuddleID = "h1"
		snap.Actors["lewis"] = &sim.ActorSnapshot{
			DisplayName: "Lewis Walker", Kind: sim.KindNPCStateful,
			CurrentHuddleID: "h1", State: sim.StateLaboring,
		}
		snap.Huddles = map[sim.HuddleID]*sim.Huddle{
			"h1": {ID: "h1", Members: map[sim.ActorID]struct{}{"prudence": {}, "lewis": {}}},
		}
		snap.LaborLedger = map[sim.LaborID]*sim.LaborOffer{
			1: {ID: 1, WorkerID: "lewis", EmployerID: "prudence", State: sim.LaborStateWorking, WorkingUntil: &laborUntil},
		}
		p := Build(snap, "prudence", nil)
		if p.Forage == nil {
			t.Fatal("expected the forage cue to survive: a worker mid-job is not custom (LLM-231)")
		}
		if p.DutySteer == nil || !p.DutySteer.AtPost || p.DutySteer.ForageErrand != ForageErrandOwnBushes {
			t.Fatalf("expected the at-post stabilizer reframed as a step-out errand, got %+v", p.DutySteer)
		}
	})

	// The narrowing is only about who counts, not about dropping the guard: one
	// real companion still defers the cue even with a laboring worker in the room.
	t.Run("hired worker plus an ordinary companion -> Forage still deferred", func(t *testing.T) {
		snap, seller := base()
		seller.CurrentHuddleID = "h1"
		snap.Actors["lewis"] = &sim.ActorSnapshot{
			DisplayName: "Lewis Walker", Kind: sim.KindNPCStateful,
			CurrentHuddleID: "h1", State: sim.StateLaboring,
		}
		snap.Actors["mary"] = &sim.ActorSnapshot{DisplayName: "Goodwife Mary", Kind: sim.KindNPCStateful, CurrentHuddleID: "h1"}
		snap.Huddles = map[sim.HuddleID]*sim.Huddle{
			"h1": {ID: "h1", Members: map[sim.ActorID]struct{}{"prudence": {}, "lewis": {}, "mary": {}}},
		}
		snap.LaborLedger = map[sim.LaborID]*sim.LaborOffer{
			1: {ID: 1, WorkerID: "lewis", EmployerID: "prudence", State: sim.LaborStateWorking, WorkingUntil: &laborUntil},
		}
		p := Build(snap, "prudence", nil)
		if p.Forage != nil {
			t.Fatal("expected forage deferred: Mary is custom even though Lewis is not")
		}
	})

	// A peer mid-job for SOMEONE ELSE is not custom either — the subject is not in
	// an encounter with a stranger who is busy working. Same read as LLM-231, which
	// drops a laboring peer as a sale target regardless of who employs them.
	t.Run("someone else's worker mid-job at own post -> Forage survives", func(t *testing.T) {
		snap, seller := base()
		seller.CurrentHuddleID = "h1"
		snap.Actors["silence"] = &sim.ActorSnapshot{
			DisplayName: "Silence Walker", Kind: sim.KindNPCStateful,
			CurrentHuddleID: "h1", State: sim.StateLaboring,
		}
		snap.Actors["abraham"] = &sim.ActorSnapshot{DisplayName: "Abraham Warren", Kind: sim.KindNPCStateful}
		snap.Huddles = map[sim.HuddleID]*sim.Huddle{
			"h1": {ID: "h1", Members: map[sim.ActorID]struct{}{"prudence": {}, "silence": {}}},
		}
		snap.LaborLedger = map[sim.LaborID]*sim.LaborOffer{
			1: {ID: 1, WorkerID: "silence", EmployerID: "abraham", State: sim.LaborStateWorking, WorkingUntil: &laborUntil},
		}
		p := Build(snap, "prudence", nil)
		if p.Forage == nil {
			t.Fatal("expected the forage cue to survive beside a peer working for a third party")
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
	if !strings.Contains(out, `Use move_to with destination "bushB" to walk out to them.`) {
		t.Errorf("forage cue must steer move_to:\n%s", out)
	}
}

// bushAssetID / bushAsset give a placed bush a resolvable loiter pin. No door and
// FootprintBottom 0, so computeLoiterTile's footprint fallback puts the pin at
// anchor + (0, 2) — the tile move_to parks a walker on, and the tile the LLM-617
// already-at guard measures against.
const bushAssetID = sim.AssetID("bush-asset")

func bushAssets() map[sim.AssetID]*sim.Asset {
	return map[sim.AssetID]*sim.Asset{bushAssetID: {FootprintBottom: 0}}
}

// bushPin is where a bush anchored at `anchor` parks a walker.
func bushPin(anchor sim.TilePos) sim.TilePos {
	return anchor.Add(sim.TileOffset{DX: 0, DY: 2})
}

// forageBushAt is forageBush placed at a tile with a real asset, so the LLM-617
// already-at guard can resolve its loiter pin. The bare forageBush carries no
// asset on purpose (see TestBuildForage_BushWithNoAsset_StillSteers).
func forageBushAt(owner sim.ActorID, item sim.ItemKind, avail int, anchor sim.TilePos) *sim.VillageObject {
	b := forageBush(owner, item, avail)
	b.AssetID = bushAssetID
	b.Pos = anchor.Center()
	return b
}

// TestBuildForage_SkipsBushTheGrowerAlreadyStandsAt is the LLM-617 regression —
// the live Moses James wedge. He stood at his wheat field for two hours re-calling
// move_to on the bush he was already at: move_to bounced it as a no-op
// (TerminalNoOpError), the tick ended on that terminal verb, and the unchanged cue
// re-issued the same handle forever.
//
// bushA is the RIPEST, so the pre-fix ripest-wins rule named it — and the grower is
// standing on its pin. The handle must skip it for the farther bushB, which move_to
// will actually walk. Ripe counts still describe the whole plot (14), not just the
// steerable part: the counts report the farm, the handle reports the errand.
func TestBuildForage_SkipsBushTheGrowerAlreadyStandsAt(t *testing.T) {
	anchorA := sim.TilePos{X: 10, Y: 87}
	subj := &sim.ActorSnapshot{
		Pos:           bushPin(anchorA), // standing exactly where move_to parks him at bushA
		Inventory:     map[sim.ItemKind]int{"wheat": 0},
		RestockPolicy: foragePolicy("wheat", 30),
		KnownPlaces:   remembersGather("wheat", "bushA", "bushB"),
	}
	snap := &sim.Snapshot{
		Actors: map[sim.ActorID]*sim.ActorSnapshot{"moses": subj},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			"bushA": forageBushAt("moses", "wheat", 9, anchorA),
			"bushB": forageBushAt("moses", "wheat", 5, sim.TilePos{X: 40, Y: 40}),
		},
		Assets:            bushAssets(),
		RestockReorderPct: 25,
	}
	v := buildForage(snap, "moses", subj, false)
	if v == nil || len(v.Items) != 1 {
		t.Fatalf("expected one item, got %+v", v)
	}
	got := v.Items[0]
	if got.MoveHandle != "bushB" {
		t.Fatalf("MoveHandle = %q, want \"bushB\" — the ripest bush is underfoot, so the steer must name one the grower can actually walk to (LLM-617)", got.MoveHandle)
	}
	if got.AtRipeBush {
		t.Errorf("AtRipeBush must stay false while a walkable ripe bush remains — it only explains an EMPTY handle")
	}
	if got.RipeUnits != 14 {
		t.Errorf("RipeUnits = %d, want 14 — the counts describe the whole plot, including the bush underfoot", got.RipeUnits)
	}
	if got.BushCount != 2 {
		t.Errorf("BushCount = %d, want 2", got.BushCount)
	}
}

// TestBuildForage_AllRipeBushesUnderfoot_FlagsInsteadOfSteering is the harder arm:
// every ripe bush is one the grower already stands at, so there is no walk left to
// name. The handle must be empty and AtRipeBush set, so render can say where he is
// instead of emitting a move_to the command would bounce.
func TestBuildForage_AllRipeBushesUnderfoot_FlagsInsteadOfSteering(t *testing.T) {
	anchor := sim.TilePos{X: 10, Y: 87}
	subj := &sim.ActorSnapshot{
		Pos:           bushPin(anchor),
		Inventory:     map[sim.ItemKind]int{"wheat": 0},
		RestockPolicy: foragePolicy("wheat", 30),
		KnownPlaces:   remembersGather("wheat", "bushA"),
	}
	snap := &sim.Snapshot{
		Actors: map[sim.ActorID]*sim.ActorSnapshot{"moses": subj},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			"bushA": forageBushAt("moses", "wheat", 4, anchor),
		},
		Assets:            bushAssets(),
		RestockReorderPct: 25,
	}
	v := buildForage(snap, "moses", subj, false)
	if v == nil || len(v.Items) != 1 {
		t.Fatalf("expected one item, got %+v", v)
	}
	got := v.Items[0]
	if got.MoveHandle != "" {
		t.Fatalf("MoveHandle = %q, want empty — move_to would bounce a walk to the bush underfoot (LLM-617)", got.MoveHandle)
	}
	if !got.AtRipeBush {
		t.Fatalf("AtRipeBush must be set so render can tell this apart from the none-ripe case")
	}
	if got.RipeUnits != 4 {
		t.Errorf("RipeUnits = %d, want 4 — there IS ripe stock, it is just underfoot", got.RipeUnits)
	}
}

// TestBuildForage_BushWithNoAsset_StillSteers pins the guard's fail-open edge.
// move_to's own no-op check resolves no pin for a dangling asset and lets the walk
// through, so the cue must keep steering there too. Filtering on an unresolvable
// pin would silently strip the only handle a grower had.
func TestBuildForage_BushWithNoAsset_StillSteers(t *testing.T) {
	subj := &sim.ActorSnapshot{
		Pos:           bushPin(sim.TilePos{X: 10, Y: 87}),
		Inventory:     map[sim.ItemKind]int{"wheat": 0},
		RestockPolicy: foragePolicy("wheat", 30),
		KnownPlaces:   remembersGather("wheat", "bushA"),
	}
	bush := forageBush("moses", "wheat", 4) // no AssetID, no Pos
	snap := &sim.Snapshot{
		Actors:            map[sim.ActorID]*sim.ActorSnapshot{"moses": subj},
		VillageObjects:    map[sim.VillageObjectID]*sim.VillageObject{"bushA": bush},
		Assets:            bushAssets(),
		RestockReorderPct: 25,
	}
	v := buildForage(snap, "moses", subj, false)
	if v == nil || len(v.Items) != 1 {
		t.Fatalf("expected one item, got %+v", v)
	}
	if v.Items[0].MoveHandle != "bushA" {
		t.Fatalf("MoveHandle = %q, want \"bushA\" — an unresolvable pin must fail OPEN, matching move_to", v.Items[0].MoveHandle)
	}
}

// TestRenderForage_AtRipeBush_SaysStandingNotWalk pins the LLM-617 render arm. The
// at-bush line must not steer move_to (the command would bounce it), must not name
// gather (the LLM-59/79 posture — the at-bush proximity cue owns that verb), and
// must not fall through to "none ripe yet", which would contradict its own count.
func TestRenderForage_AtRipeBush_SaysStandingNotWalk(t *testing.T) {
	v := &ForageView{Items: []ForageItemView{
		{ItemLabel: "wheat", CurrentQty: 0, Cap: 30, BushCount: 51, RipeUnits: 6, MoveHandle: "", AtRipeBush: true},
	}}
	var b strings.Builder
	renderForage(&b, v)
	out := b.String()
	if strings.Contains(out, "move_to") {
		t.Errorf("must not steer move_to at a bush already underfoot — that is the LLM-617 wedge:\n%s", out)
	}
	if strings.Contains(out, "gather") {
		t.Errorf("must not name the gather tool (LLM-59/79 steering posture):\n%s", out)
	}
	if strings.Contains(out, "none ripe yet") {
		t.Errorf("must not claim nothing is ripe while reporting 6 ripe:\n%s", out)
	}
	if !strings.Contains(out, "6 ripe to pick now") {
		t.Errorf("must still report the ripe count:\n%s", out)
	}
	if !strings.Contains(out, "standing among them") {
		t.Errorf("must say where the grower is:\n%s", out)
	}
}

// TestBuildForage_ExplicitOffsetsButNoAsset_StillSteers pins the exact edge
// code_review raised on LLM-617: an object carrying explicit per-instance loiter
// offsets but NO resolvable asset.
//
// The expected result is defined by move_to, not by what looks intuitive. Its
// no-op guard goes through sim.ObjectLoiterPin, which requires the asset even when
// an explicit override is present — so move_to does NOT consider the actor already
// there and lets the walk proceed. The cue must therefore keep steering, and the
// grower standing exactly on the override pin must NOT suppress the handle.
//
// Getting this backwards would make the cue stricter than the command: it would
// drop the only handle for such an object and leave the grower with a section that
// steers nowhere.
func TestBuildForage_ExplicitOffsetsButNoAsset_StillSteers(t *testing.T) {
	zero := 0
	anchor := sim.TilePos{X: 10, Y: 87}
	subj := &sim.ActorSnapshot{
		Pos:           anchor, // standing ON the explicit (0,0) override pin
		Inventory:     map[sim.ItemKind]int{"wheat": 0},
		RestockPolicy: foragePolicy("wheat", 30),
		KnownPlaces:   remembersGather("wheat", "bushA"),
	}
	bush := forageBush("moses", "wheat", 4)
	bush.AssetID = "no-such-asset" // dangling: absent from the Assets map below
	bush.Pos = anchor.Center()
	bush.LoiterOffsetX = &zero
	bush.LoiterOffsetY = &zero
	snap := &sim.Snapshot{
		Actors:            map[sim.ActorID]*sim.ActorSnapshot{"moses": subj},
		VillageObjects:    map[sim.VillageObjectID]*sim.VillageObject{"bushA": bush},
		Assets:            bushAssets(), // does NOT contain "no-such-asset"
		RestockReorderPct: 25,
	}
	// Ground the expectation in the shared resolver rather than restating it: if
	// ObjectLoiterPin ever starts resolving an assetless object, move_to's no-op
	// guard changes with it and this test's premise must be revisited.
	if _, ok := sim.ObjectLoiterPin(snap.VillageObjects, snap.Assets, "bushA"); ok {
		t.Fatalf("premise broken: ObjectLoiterPin now resolves an assetless object — move_to's no-op guard changed, so revisit this test and growerStandsAtBush")
	}
	v := buildForage(snap, "moses", subj, false)
	if v == nil || len(v.Items) != 1 {
		t.Fatalf("expected one item, got %+v", v)
	}
	if v.Items[0].MoveHandle != "bushA" {
		t.Fatalf("MoveHandle = %q, want \"bushA\" — move_to would still walk here, so the cue must not be stricter than the command", v.Items[0].MoveHandle)
	}
	if v.Items[0].AtRipeBush {
		t.Errorf("AtRipeBush must stay false when the pin is unresolvable")
	}
}
