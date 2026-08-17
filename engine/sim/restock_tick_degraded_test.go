package sim

import (
	"testing"
	"time"
)

// restock_tick_degraded_test.go — LLM-608, the warrant half. buildRestocking now
// keeps the inputs a degraded keeper's OWN production consumes, so the warrant
// producer has to wake him for exactly those and nothing else. A cue that renders
// on a wake that never comes is as silent as no cue at all; a wake whose cue
// declines to render is the loop LLM-260 removed. Both halves read
// ProductionInputKinds so they cannot drift apart.

// degradedMaker returns a maker of "stew" who buys "skillet" for it and resells
// "ale", holding `skillet` and `ale` at `onHand`, plus a world where his own
// business is worn past the degrade threshold and the shop LIMPS (the live
// StallDegradedProducePct 50, not the legacy 0 full block).
func degradedMaker(onHand int) (*Actor, *World) {
	a := &Actor{
		ID:        "smith",
		Kind:      KindNPCStateful,
		LLMAgent:  "smith-agent",
		Coins:     50,
		Inventory: map[ItemKind]int{"skillet": onHand, "ale": onHand},
		RestockPolicy: &RestockPolicy{Restock: []RestockEntry{
			{Item: "stew", Source: RestockSourceProduce, Max: 60},
			{Item: "skillet", Source: RestockSourceBuy, Max: 20},
			{Item: "ale", Source: RestockSourceBuy, Max: 20},
		}},
	}
	w := restockWorld(a)
	w.Recipes = map[ItemKind]*ItemRecipe{
		"stew": {
			OutputItem: "stew", OutputQty: 30, RateQty: 30, RatePerHours: 6,
			Inputs: []RecipeInput{{Item: "skillet", Qty: 1}},
		},
	}
	addSupplier(w, "potter", "skillet")
	addSupplier(w, "brewer", "ale")
	w.Settings.StallWearDegradeThreshold = 600
	w.Settings.StallDegradedProducePct = 50
	if w.VillageObjects == nil {
		w.VillageObjects = map[VillageObjectID]*VillageObject{}
	}
	w.VillageObjects["smiths_shop"] = &VillageObject{
		ID: "smiths_shop", OwnerActorID: "smith", Tags: []string{TagBusiness}, Wear: 650,
	}
	return a, w
}

// TestDegradePredicatesAgreeAcrossTheSnapshot pins that the INPUTS to the two
// degrade predicates stay equivalent. The warrant half reads the live World
// (ownerStallDegraded / degradedProduceBlocked); the perception half reads a
// published Snapshot (perception.ownerBusinessDegraded /
// ownerBusinessProduceBlocked). Both are wrappers over the SAME exported
// primitives — StallDegraded over OwnedWearableStall, against the degrade
// threshold — so the only way they can disagree is the snapshot losing fidelity:
// a dropped Wear or Tags on the object clone, or a settings field the snapshot
// builder forgets to carry.
//
// It RECONSTRUCTS the snapshot-side computation rather than calling perception's
// wrappers, because perception imports sim and the dependency cannot run the
// other way. So it does not prove the two wrappers stay in step with each other —
// it proves the world and the snapshot keep telling them the same thing, which is
// the failure this split-across-two-packages narrowing can actually produce.
// Divergence in the wrappers themselves is covered by their own package's tests.
//
// The wear cases include the exact threshold, since StallDegraded's `>=` is the
// load-bearing comparison and an accidental `>` would otherwise slip through.
func TestDegradePredicatesAgreeAcrossTheSnapshot(t *testing.T) {
	for _, tc := range []struct {
		name       string
		wear       int
		producePct int
	}{
		{"worn, shop limps", 650, 50},
		{"worn, production hard-blocked", 650, 0},
		{"exactly at the threshold", 600, 50},
		{"one below the threshold", 599, 50},
		{"mended", 0, 50},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, w := degradedMaker(3)
			w.VillageObjects["smiths_shop"].Wear = tc.wear
			w.Settings.StallDegradedProducePct = tc.producePct
			w.republish()
			snap := w.Published()

			// Exactly what perception.ownerBusinessDegraded / ownerBusinessProduceBlocked do.
			snapDegraded := StallDegraded(OwnedWearableStall(snap.VillageObjects, "smith"), snap.StallWearDegradeThreshold)
			snapBlocked := snap.StallDegradedProducePct <= 0 && snapDegraded

			if got := ownerStallDegraded(w, "smith"); got != snapDegraded {
				t.Errorf("degraded: world says %v, snapshot says %v — the two halves would disagree about what may be bought", got, snapDegraded)
			}
			if got := degradedProduceBlocked(w, "smith"); got != snapBlocked {
				t.Errorf("produce-blocked: world says %v, snapshot says %v", got, snapBlocked)
			}
		})
	}
}

// A degraded maker IS woken to buy the input his own production consumes — the
// wake that carries him to the water that makes the nail that mends the forge.
// The representative item must be that input, never the resale good.
//
// The supplier here is at his OWN workplace and shares no huddle with the smith,
// so this is deliberately the WALK-TO case: the arm that breaks the deadlock when
// nobody happens to be standing in the room, and the one this fix keeps on purpose.
func TestEvaluateRestock_DegradedWarrantsProductionInput(t *testing.T) {
	a, w := degradedMaker(3) // 3/20 = 15% < 25% on both items
	if a.CurrentHuddleID != "" || w.Actors["potter"].CurrentHuddleID != "" {
		t.Fatal("fixture drift: this case must exercise the walk-to arm, with no co-present seller")
	}
	now := time.Now().UTC()

	res, err := EvaluateRestock(now).Fn(w)
	if err != nil {
		t.Fatalf("EvaluateRestock: %v", err)
	}
	if res.(int) != 1 {
		t.Fatalf("stamped = %d, want 1 (a degraded maker still reorders his own inputs)", res.(int))
	}
	if !hasWarrantKind(a, WarrantKindRestock) {
		t.Fatalf("expected a restock warrant; kinds = %v", warrantKinds(a))
	}
	for _, m := range a.Warrants {
		if r, ok := m.Reason.(RestockWarrantReason); ok && r.Item != "skillet" {
			t.Errorf("warrant item = %q, want the production input skillet", r.Item)
		}
	}
}

// With the input stocked and only the RESALE good low, there is nothing a
// degraded keeper may act on — LLM-304 stands, and he is not woken for shelves he
// cannot refill.
func TestEvaluateRestock_DegradedNoWarrantForResaleAlone(t *testing.T) {
	a, w := degradedMaker(3)
	a.Inventory["skillet"] = 20 // at cap — only `ale` is low now
	now := time.Now().UTC()

	res, err := EvaluateRestock(now).Fn(w)
	if err != nil {
		t.Fatalf("EvaluateRestock: %v", err)
	}
	if res.(int) != 0 {
		t.Errorf("stamped = %d, want 0 (resale stock waits on the mending)", res.(int))
	}
	if hasWarrantKind(a, WarrantKindRestock) {
		t.Fatalf("resale stock must not warrant while worn; kinds = %v", warrantKinds(a))
	}
}

// At StallDegradedProducePct 0 the legacy full block applies — production is
// refused outright, so its inputs warrant nothing either. Foil of
// TestEvaluateRestock_DegradedWarrantsProductionInput: identical but hard-blocked.
func TestEvaluateRestock_DegradedProduceBlockedNoWarrant(t *testing.T) {
	a, w := degradedMaker(3)
	w.Settings.StallDegradedProducePct = 0
	now := time.Now().UTC()

	res, err := EvaluateRestock(now).Fn(w)
	if err != nil {
		t.Fatalf("EvaluateRestock: %v", err)
	}
	if res.(int) != 0 {
		t.Errorf("stamped = %d, want 0 (production hard-blocked)", res.(int))
	}
	if hasWarrantKind(a, WarrantKindRestock) {
		t.Fatalf("a hard-blocked business must not get a restock warrant; kinds = %v", warrantKinds(a))
	}
}

// The forage side wakes while worn (LLM-634). LLM-304 shut it along with the buy
// side, and that deadlocked the whole village when the sole water gatherer's Mill
// degraded (2026-08-15→17): water makes the nail that mends every business, so
// blocking the gather removed the village's only exit from degrade. Foraging
// spends no coin — the LLM-304 "wasted errand" argument does not reach it — and
// buildForage never gated on degrade, so the wake was the missing half of the
// lockstep pair. Both produce settings are covered because foraging is not
// production: the wake must not ride the LLM-608 buy carve-out, which does switch
// off at the legacy pct-0 full block.
func TestEvaluateRestock_DegradedForageStillWarrants(t *testing.T) {
	for _, tc := range []struct {
		name       string
		producePct int
	}{
		{"shop limps (live pct 50)", 50},
		{"production hard-blocked (legacy pct 0)", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, w := degradedMaker(3)
			w.Settings.StallDegradedProducePct = tc.producePct
			a.Inventory["skillet"] = 20 // input at cap, so only the forage entry could fire
			a.RestockPolicy.Restock = append(a.RestockPolicy.Restock,
				RestockEntry{Item: "berries", Source: RestockSourceForage, Max: 20})
			a.Inventory["berries"] = 1
			w.VillageObjects["bush"] = forageBushObj("smith", "berries", 10)
			rememberForageBush(a, "berries", "bush")
			now := time.Now().UTC()

			res, err := EvaluateRestock(now).Fn(w)
			if err != nil {
				t.Fatalf("EvaluateRestock: %v", err)
			}
			if res.(int) != 1 {
				t.Fatalf("stamped = %d, want 1 (a degraded keeper is still woken to gather)", res.(int))
			}
			if !hasWarrantKind(a, WarrantKindRestock) {
				t.Fatalf("expected a restock warrant; kinds = %v", warrantKinds(a))
			}
			for _, m := range a.Warrants {
				if r, ok := m.Reason.(RestockWarrantReason); ok {
					if r.Item != "berries" {
						t.Errorf("warrant item = %q, want the forage item berries", r.Item)
					}
					if r.Source != RestockSourceForage {
						t.Errorf("warrant source = %q, want %q", r.Source, RestockSourceForage)
					}
				}
			}
		})
	}
}
