package sim

import (
	"testing"
	"time"
)

// stall_wear_internal_test.go — LLM-118. Internal (package sim) tests for the
// accrual math + edge-triggered warrant and the scope/threshold predicates. These
// reach the unexported accrueStallWear / sellerStallDegraded directly against a
// hand-built World, so they don't need the full pay fixture.

func stallTestWorld(perCoin, repairThr, degradeThr, wear int) (*World, *Actor, *VillageObject) {
	owner := &Actor{ID: "ezekiel", Kind: KindNPCStateful, LLMAgent: "ezekiel"}
	stall := &VillageObject{
		ID:           "stall",
		OwnerActorID: owner.ID,
		Tags:         []string{TagMarketStall},
		Wear:         wear,
	}
	w := &World{
		Settings: WorldSettings{
			StallWearPerCoin:          perCoin,
			StallWearRepairThreshold:  repairThr,
			StallWearDegradeThreshold: degradeThr,
			MaxWarrantsPerActor:       16,
		},
		Actors:         map[ActorID]*Actor{owner.ID: owner},
		VillageObjects: map[VillageObjectID]*VillageObject{stall.ID: stall},
	}
	return w, owner, stall
}

func hasStallRepairWarrant(a *Actor) bool {
	for _, wm := range a.Warrants {
		if _, ok := wm.Reason.(StallRepairWarrantReason); ok {
			return true
		}
	}
	return false
}

func TestAccrueStallWear_MathAndEdgeWarrant(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()

	// Money-weighted accrual: Wear += amount * perCoin. Crossing the repair
	// threshold stamps the one-shot warrant.
	w, owner, stall := stallTestWorld(2, 400, 600, 390)
	accrueStallWear(w, owner, 10, now) // 390 + 10*2 = 410, crosses 400
	if stall.Wear != 410 {
		t.Fatalf("Wear = %d, want 410", stall.Wear)
	}
	if !hasStallRepairWarrant(owner) {
		t.Error("expected a stall-repair warrant on the upward crossing")
	}

	// Already past the threshold: accrues, but does NOT re-stamp (edge-trigger).
	w2, owner2, stall2 := stallTestWorld(1, 400, 600, 500)
	accrueStallWear(w2, owner2, 50, now)
	if stall2.Wear != 550 {
		t.Fatalf("Wear = %d, want 550", stall2.Wear)
	}
	if hasStallRepairWarrant(owner2) {
		t.Error("no warrant expected when already above the threshold (no upward crossing)")
	}
}

func TestAccrueStallWear_NoOps(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()

	// perCoin == 0 disables wear entirely.
	w, owner, stall := stallTestWorld(0, 400, 600, 100)
	accrueStallWear(w, owner, 100, now)
	if stall.Wear != 100 || hasStallRepairWarrant(owner) {
		t.Errorf("perCoin=0 should be a no-op: Wear=%d warrant=%v", stall.Wear, hasStallRepairWarrant(owner))
	}

	// amount == 0 (a pure-barter sale) accrues nothing.
	w2, owner2, stall2 := stallTestWorld(1, 400, 600, 100)
	accrueStallWear(w2, owner2, 0, now)
	if stall2.Wear != 100 {
		t.Errorf("amount=0 should accrue nothing: Wear=%d", stall2.Wear)
	}

	// Seller owns no wearable stall: no panic, no-op. Drop the tag so the stall
	// no longer scopes in.
	w3, owner3, stall3 := stallTestWorld(1, 400, 600, 100)
	stall3.Tags = nil
	accrueStallWear(w3, owner3, 100, now)
	if stall3.Wear != 100 {
		t.Errorf("untagged stall should not wear: Wear=%d", stall3.Wear)
	}
}

func TestStallWearPredicates(t *testing.T) {
	owned := &VillageObject{ID: "s", OwnerActorID: "ezekiel", Tags: []string{TagMarketStall}, Wear: 450}
	unowned := &VillageObject{ID: "u", Tags: []string{TagMarketStall}, Wear: 999}
	untagged := &VillageObject{ID: "n", OwnerActorID: "ezekiel", Wear: 999}

	if !IsWearableStall(owned) {
		t.Error("owned + tagged should be a wearable stall")
	}
	if IsWearableStall(unowned) {
		t.Error("unowned stall is not wearable (no owner to mend it)")
	}
	if IsWearableStall(untagged) {
		t.Error("untagged object is not a wearable stall")
	}
	if IsWearableStall(nil) {
		t.Error("nil is not a wearable stall")
	}

	if !StallNeedsRepair(owned, 400) || StallNeedsRepair(owned, 500) {
		t.Error("StallNeedsRepair should be true at/above the threshold, false below")
	}
	if StallNeedsRepair(owned, 0) {
		t.Error("a non-positive repair threshold disables the transition")
	}
	if !StallDegraded(owned, 400) || StallDegraded(owned, 600) {
		t.Error("StallDegraded should be true at/above the threshold, false below")
	}

	objects := map[VillageObjectID]*VillageObject{"s": owned, "u": unowned}
	if got := OwnedWearableStall(objects, "ezekiel"); got != owned {
		t.Errorf("OwnedWearableStall = %v, want the owned stall", got)
	}
	if got := OwnedWearableStall(objects, "nobody"); got != nil {
		t.Errorf("OwnedWearableStall for a non-owner = %v, want nil", got)
	}

	wDeg := &World{
		Settings:       WorldSettings{StallWearDegradeThreshold: 600},
		VillageObjects: map[VillageObjectID]*VillageObject{"s": {ID: "s", OwnerActorID: "ezekiel", Tags: []string{TagMarketStall}, Wear: 650}},
	}
	if !sellerStallDegraded(wDeg, "ezekiel") {
		t.Error("seller with a 650-wear stall (degrade 600) should be degraded")
	}
	if sellerStallDegraded(wDeg, "nobody") {
		t.Error("a seller who owns no stall is never degraded")
	}
}
