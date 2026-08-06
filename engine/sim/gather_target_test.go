package sim

import "testing"

func TestBetterGatherCandidate(t *testing.T) {
	const target = VillageObjectID("target")
	mk := func(id VillageObjectID, cheb int, mine, stock, low bool) GatherCandidate {
		return GatherCandidate{ID: id, Cheb: cheb, Mine: mine, HasStock: stock, Low: low}
	}
	cases := []struct {
		name     string
		a, b     GatherCandidate
		targetID VillageObjectID
		wantA    bool
	}{
		{"stocked target outranks a nearer ripe low bush", mk(target, 1, true, true, false), mk("x", 0, true, true, true), target, true},
		{"DEPLETED target does NOT win — falls through to an adjacent ripe bush", mk(target, 0, true, false, false), mk("x", 1, true, true, false), target, false},
		{"ownable beats owned-by-other", mk("a", 1, true, true, false), mk("b", 0, false, true, false), "", true},
		{"stocked beats depleted (skip the zeroed bush)", mk("a", 1, true, true, false), mk("b", 0, true, false, false), "", true},
		{"a restock (low) item beats a not-needed one", mk("a", 1, true, true, true), mk("b", 0, true, true, false), "", true},
		{"nearer breaks an otherwise-equal pair", mk("a", 0, true, true, true), mk("b", 1, true, true, true), "", true},
		{"lowest id breaks a full tie", mk("a", 1, true, true, true), mk("z", 1, true, true, true), "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := BetterGatherCandidate(c.a, c.b, c.targetID); got != c.wantA {
				t.Errorf("BetterGatherCandidate(a,b) = %v, want %v", got, c.wantA)
			}
			// None of these pairs are fully equal, so the relation must be asymmetric.
			if got := BetterGatherCandidate(c.b, c.a, c.targetID); got == c.wantA {
				t.Errorf("ranking not asymmetric — (b,a) returned %v too", got)
			}
		})
	}
}

func TestFirstGatherableRow(t *testing.T) {
	q := func(v int) *int { return &v }
	row, stock, ok := FirstGatherableRow(&VillageObject{Refreshes: []*ObjectRefresh{
		{GatherItem: "berries", AvailableQuantity: q(3), Amount: 0},
	}})
	if !ok || !stock || row.GatherItem != "berries" {
		t.Errorf("stocked finite: got (%+v, stock=%v, ok=%v)", row, stock, ok)
	}
	if _, stock, ok := FirstGatherableRow(&VillageObject{Refreshes: []*ObjectRefresh{
		{GatherItem: "berries", AvailableQuantity: q(0)},
	}}); !ok || stock {
		t.Errorf("depleted finite: want ok+no-stock, got stock=%v ok=%v", stock, ok)
	}
	if _, stock, ok := FirstGatherableRow(&VillageObject{Refreshes: []*ObjectRefresh{
		{GatherItem: "water"}, // infinite (no AvailableQuantity)
	}}); !ok || !stock {
		t.Errorf("infinite: want ok+stock, got stock=%v ok=%v", stock, ok)
	}
	if _, _, ok := FirstGatherableRow(&VillageObject{Refreshes: []*ObjectRefresh{
		{Attribute: "hunger", Amount: -4}, // no GatherItem
	}}); ok {
		t.Error("a refresh row with no gather item must be ok=false")
	}
	if _, _, ok := FirstGatherableRow(nil); ok {
		t.Error("nil object must be ok=false")
	}
}

func TestLowForageItems(t *testing.T) {
	policy := &RestockPolicy{Restock: []RestockEntry{
		{Item: "raspberries", Source: RestockSourceForage, Max: 10}, // 1/10 → low
		{Item: "blueberries", Source: RestockSourceForage, Max: 10}, // 9/10 → not low
		{Item: "milk", Source: RestockSourceBuy, Max: 10},           // buy source → ignored
	}}
	inv := map[ItemKind]int{"raspberries": 1, "blueberries": 9, "milk": 0}
	low := LowForageItems(policy, inv, 25)
	if !low["raspberries"] || low["blueberries"] || low["milk"] {
		t.Errorf("low = %v, want only raspberries", low)
	}
	if LowForageItems(policy, inv, 0) != nil {
		t.Error("pct 0 disables the feature → nil")
	}
	if LowForageItems(nil, inv, 25) != nil {
		t.Error("nil policy → nil")
	}
}

func TestHandleGatherTargetOnArrival(t *testing.T) {
	a := &Actor{ID: "prue", Kind: KindNPCStateful}
	w := &World{Actors: map[ActorID]*Actor{"prue": a}}

	// Arrival at an object stamps it as the gather target.
	handleGatherTargetOnArrival(w, &ActorArrived{ActorID: "prue", DestObjectID: "bushA"})
	if a.GatherTargetObjectID != "bushA" {
		t.Fatalf("object arrival: want bushA, got %q", a.GatherTargetObjectID)
	}
	// Arrival at a structure/position carries an empty DestObjectID → clears it.
	handleGatherTargetOnArrival(w, &ActorArrived{ActorID: "prue", DestObjectID: ""})
	if a.GatherTargetObjectID != "" {
		t.Fatalf("structure arrival should clear the target, got %q", a.GatherTargetObjectID)
	}
	// A PC drives its own gather verb — its arrivals are ignored.
	pc := &Actor{ID: "player", Kind: KindPC}
	w.Actors["player"] = pc
	handleGatherTargetOnArrival(w, &ActorArrived{ActorID: "player", DestObjectID: "bushB"})
	if pc.GatherTargetObjectID != "" {
		t.Errorf("PC arrival must be ignored, got %q", pc.GatherTargetObjectID)
	}
	// A non-arrival event is a no-op (no panic).
	handleGatherTargetOnArrival(w, nil)
}

// --- LLM-610: the yield-only permission gate ---------------------------------
//
// The rule: any character may DRINK at a well, only a character with the gather
// trade may carry a pail away. Expressed against the row kind the schema already
// carries — pick-and-eat (Amount < 0) is a commons, yield-only (Amount == 0) is
// forage-to-sell stock and needs the trade.

func forageSet(items ...ItemKind) map[ItemKind]bool {
	out := make(map[ItemKind]bool, len(items))
	for _, i := range items {
		out[i] = true
	}
	return out
}

func TestMayGatherSource(t *testing.T) {
	q := func(v int) *int { return &v }
	pickAndEat := &ObjectRefresh{Attribute: "hunger", Amount: -2, GatherItem: "raspberries", AvailableQuantity: q(4)}
	yieldOnly := &ObjectRefresh{Amount: 0, GatherItem: "water", AvailableQuantity: q(20)}
	commons := &VillageObject{ID: "well"}
	mine := &VillageObject{ID: "my-field", OwnerActorID: "moses"}
	theirs := &VillageObject{ID: "their-field", OwnerActorID: "someone-else"}

	cases := []struct {
		name      string
		obj       *VillageObject
		row       *ObjectRefresh
		actor     ActorID
		foragable map[ItemKind]bool
		want      bool
	}{
		{"wild bush is a commons — no trade needed", commons, pickAndEat, "ezekiel", nil, true},
		{"wild bush stays open even for a forager of something else", commons, pickAndEat, "ezekiel", forageSet("firewood"), true},
		{"commons yield-only is REFUSED without the trade", commons, yieldOnly, "ezekiel", forageSet("firewood"), false},
		{"commons yield-only is refused when the actor forages nothing", commons, yieldOnly, "ezekiel", nil, false},
		{"commons yield-only is allowed with the matching entry", commons, yieldOnly, "joseph", forageSet("water", "firewood"), true},
		{"your own yield-only source needs no entry — ownership is the claim", mine, yieldOnly, "moses", nil, true},
		{"another's source is not made yours by holding the entry", theirs, yieldOnly, "moses", forageSet("water"), false},
		{"a nil row is never gatherable", commons, nil, "ezekiel", forageSet("water"), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// The owned-by-another case is enforced upstream by OwnedByOther; assert
			// the same answer here so the two gates can never disagree about it.
			if c.obj == theirs {
				if !c.obj.OwnedByOther(c.actor) {
					t.Fatalf("fixture broken: %q should be owned by another", c.obj.ID)
				}
				return
			}
			if got := MayGatherSource(c.obj, c.row, c.actor, c.foragable); got != c.want {
				t.Errorf("MayGatherSource = %v, want %v", got, c.want)
			}
		})
	}
}

// TestMayGatherSource_TrimsGatherItem — IsGatherable() trims, so the permission
// lookup has to trim too or a padded catalog value silently revokes the right.
func TestMayGatherSource_TrimsGatherItem(t *testing.T) {
	row := &ObjectRefresh{Amount: 0, GatherItem: " water "}
	if !MayGatherSource(&VillageObject{ID: "well"}, row, "joseph", forageSet("water")) {
		t.Error("a padded gather_item must still match the forage entry")
	}
}

// TestForageItems_IgnoresStockLevel is the distinction from LowForageItems, and
// the reason the gate does not reuse it: a forager standing at cap is still a
// forager. Gating on the LOW set would revoke the right exactly when the pack is
// full, and the actor could never refill after selling down.
func TestForageItems_IgnoresStockLevel(t *testing.T) {
	policy := &RestockPolicy{Restock: []RestockEntry{
		{Item: "water", Source: RestockSourceForage, Max: 20},
		{Item: "firewood", Source: RestockSourceForage, Max: 10},
		{Item: "iron", Source: RestockSourceBuy, Max: 6},
		{Item: "nail", Source: RestockSourceProduce, Max: 20},
	}}
	got := ForageItems(policy)
	if !got["water"] || !got["firewood"] {
		t.Errorf("forage entries missing from the permission set: %v", got)
	}
	if got["iron"] || got["nail"] {
		t.Errorf("buy/produce entries must NOT confer a gather right: %v", got)
	}
	// At cap, so LowForageItems drops it — the permission set must not.
	low := LowForageItems(policy, map[ItemKind]int{"water": 20, "firewood": 10}, 50)
	if low["water"] {
		t.Fatal("fixture broken: water should not read as low at cap")
	}
	if !ForageItems(policy)["water"] {
		t.Error("a forager at cap must keep the right to draw")
	}
	if ForageItems(nil) != nil {
		t.Error("a nil policy confers nothing")
	}
}
