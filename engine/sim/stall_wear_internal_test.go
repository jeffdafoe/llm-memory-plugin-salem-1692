package sim

import (
	"math"
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
		Tags:         []string{TagBusiness},
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

func TestAccrueStallWear_Saturates(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	// An add that would exceed int range saturates at MaxInt rather than wrapping
	// negative (which could lower Wear or un-degrade a stall). Start BELOW the degrade
	// line (500 < 600) so the LLM-304 freeze doesn't short-circuit before the add — a
	// single astronomically large sale is what pushes an under-degrade stall over int
	// range.
	w, owner, stall := stallTestWorld(1, 400, 600, 500)
	accrueStallWear(w, owner, math.MaxInt, now) // 500 + MaxInt*1 overflows int
	if stall.Wear != math.MaxInt {
		t.Errorf("Wear = %d, want saturated math.MaxInt (no negative wrap)", stall.Wear)
	}
}

// TestAccrueStallWear_FrozenWhenDegraded pins the LLM-304 freeze: once a stall is
// worn past the degrade line it is shut for restock/production, so it draws down
// rather than refilling — further sales must NOT pile on wear (repair zeroes it
// regardless). Without the freeze the number would climb unbounded as a degraded
// keeper sells down his remaining stock.
func TestAccrueStallWear_FrozenWhenDegraded(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	w, owner, stall := stallTestWorld(2, 400, 600, 650) // already degraded (650 >= 600)
	accrueStallWear(w, owner, 100, now)                 // would add 200 without the freeze
	if stall.Wear != 650 {
		t.Errorf("Wear = %d, want 650 (frozen at the degrade line — no accrual while degraded)", stall.Wear)
	}
}

func TestStallRepairable_DegradedDeBricks(t *testing.T) {
	// Misconfiguration: degrade (100) below repair (400). A stall at wear 150 is
	// degraded (sales blocked) but not "worn" to the repair line — StallRepairable
	// must still report it mendable so a bad config can't brick it.
	stall := &VillageObject{ID: "s", OwnerActorID: "ezekiel", Tags: []string{TagBusiness}, Wear: 150}
	if StallNeedsRepair(stall, 400) {
		t.Fatal("precondition: wear 150 is not 'worn' at repair threshold 400")
	}
	if !StallDegraded(stall, 100) {
		t.Fatal("precondition: wear 150 IS degraded at degrade threshold 100")
	}
	if !StallRepairable(stall, 400, 100) {
		t.Error("a degraded stall must be repairable even when degrade < repair (de-brick)")
	}
}

func TestSetStallWearSettings_Validation(t *testing.T) {
	ip := func(v int) *int { return &v }
	world := func() *World {
		return &World{Settings: WorldSettings{StallWearRepairThreshold: 400, StallWearDegradeThreshold: 600}}
	}
	bad := []struct {
		name                                      string
		perCoin, repair, degrade, nails, duration *int
	}{
		{"none provided", nil, nil, nil, nil, nil},
		{"negative perCoin", ip(-1), nil, nil, nil, nil},
		{"zero nails", nil, nil, nil, ip(0), nil},
		{"zero duration", nil, nil, nil, nil, ip(0)},
		{"degrade below repair", nil, ip(500), ip(400), nil, nil},
		{"degrade on, repair disabled", nil, ip(0), ip(400), nil, nil},
		{"partial degrade below current repair", nil, nil, ip(300), nil, nil},
	}
	for _, c := range bad {
		if _, err := SetStallWearSettings(c.perCoin, c.repair, c.degrade, c.nails, c.duration).Fn(world()); err == nil {
			t.Errorf("%s: expected rejection, got nil", c.name)
		}
	}
	w := world()
	if _, err := SetStallWearSettings(ip(2), ip(300), ip(900), ip(7), ip(120)).Fn(w); err != nil {
		t.Fatalf("valid change rejected: %v", err)
	}
	if w.Settings.StallWearPerCoin != 2 || w.Settings.StallWearDegradeThreshold != 900 {
		t.Errorf("settings not applied: %+v", w.Settings)
	}
}

func TestStallWearPredicates(t *testing.T) {
	owned := &VillageObject{ID: "s", OwnerActorID: "ezekiel", Tags: []string{TagBusiness}, Wear: 450}
	unowned := &VillageObject{ID: "u", Tags: []string{TagBusiness}, Wear: 999}
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
		VillageObjects: map[VillageObjectID]*VillageObject{"s": {ID: "s", OwnerActorID: "ezekiel", Tags: []string{TagBusiness}, Wear: 650}},
	}
	if !ownerStallDegraded(wDeg, "ezekiel") {
		t.Error("owner of a 650-wear stall (degrade 600) should be degraded")
	}
	if ownerStallDegraded(wDeg, "nobody") {
		t.Error("an actor who owns no stall is never degraded")
	}
}

// TestDefaultStallWearThresholds_LLM247 pins the recalibrated defaults so a future
// edit can't silently regress to the old LLM-118 400/600 (at which — against real
// ~50-coins/week velocity — a repair NEVER fired). Guards the constants and their
// meaning: wear at the repair default is repairable, wear at the degrade default
// is degraded, and one below repair is neither.
func TestDefaultStallWearThresholds_LLM247(t *testing.T) {
	if DefaultStallWearRepairThreshold != 60 || DefaultStallWearDegradeThreshold != 90 {
		t.Fatalf("default thresholds = repair %d / degrade %d, want 60 / 90",
			DefaultStallWearRepairThreshold, DefaultStallWearDegradeThreshold)
	}
	biz := &VillageObject{ID: "s", OwnerActorID: "ezekiel", Tags: []string{TagBusiness}}

	biz.Wear = DefaultStallWearRepairThreshold // 60
	if !StallNeedsRepair(biz, DefaultStallWearRepairThreshold) {
		t.Error("wear at the default repair threshold should be repairable")
	}
	if StallDegraded(biz, DefaultStallWearDegradeThreshold) {
		t.Error("wear at the repair threshold (60) is below degrade (90) — not degraded")
	}

	biz.Wear = DefaultStallWearRepairThreshold - 1 // 59
	if StallNeedsRepair(biz, DefaultStallWearRepairThreshold) {
		t.Error("wear just below the default repair threshold should not be repairable")
	}

	biz.Wear = DefaultStallWearDegradeThreshold // 90
	if !StallDegraded(biz, DefaultStallWearDegradeThreshold) {
		t.Error("wear at the default degrade threshold should be degraded")
	}
}
