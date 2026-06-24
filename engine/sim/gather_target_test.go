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
