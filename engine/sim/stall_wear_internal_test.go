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

// producerSale is a sale of self-produced goods: no buy history behind the item, so
// no cost basis and the whole amount is margin (LLM-411). The pre-LLM-411 accrual
// semantics, which every case below that isn't about resale still expects.
func producerSale(amount int) saleWear {
	return saleWear{
		Lines:     []QuoteLine{{ItemKind: "porridge", Qty: 1}},
		Consumers: 1,
		Amount:    amount,
		Charge:    amount,
	}
}

// boughtAt seeds the price book with a purchase the stall owner made — `units` of
// `item` for `coins`, from some other seller. This is the buy history the wear accrual
// prices its cost basis over.
func boughtAt(w *World, buyer ActorID, item ItemKind, units, coins int, at time.Time) {
	w.SeedPriceBook([]PriceBookSeedRecord{{
		Key: PriceBookKey{SellerID: "farmer", Item: item},
		Observation: PriceObservation{
			BuyerID:   buyer,
			Amount:    coins,
			Qty:       units,
			Consumers: 1,
			At:        at,
		},
	}})
}

func TestAccrueStallWear_MathAndEdgeWarrant(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()

	// Money-weighted accrual: Wear += amount * perCoin. Crossing the repair
	// threshold stamps the one-shot warrant.
	w, owner, stall := stallTestWorld(2, 400, 600, 390)
	accrueStallWear(w, owner, producerSale(10), now) // 390 + 10*2 = 410, crosses 400
	if stall.Wear != 410 {
		t.Fatalf("Wear = %d, want 410", stall.Wear)
	}
	if !hasStallRepairWarrant(owner) {
		t.Error("expected a stall-repair warrant on the upward crossing")
	}

	// Already past the threshold: accrues, but does NOT re-stamp (edge-trigger).
	w2, owner2, stall2 := stallTestWorld(1, 400, 600, 500)
	accrueStallWear(w2, owner2, producerSale(50), now)
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
	accrueStallWear(w, owner, producerSale(100), now)
	if stall.Wear != 100 || hasStallRepairWarrant(owner) {
		t.Errorf("perCoin=0 should be a no-op: Wear=%d warrant=%v", stall.Wear, hasStallRepairWarrant(owner))
	}

	// amount == 0 (a pure-barter sale) accrues nothing.
	w2, owner2, stall2 := stallTestWorld(1, 400, 600, 100)
	accrueStallWear(w2, owner2, producerSale(0), now)
	if stall2.Wear != 100 {
		t.Errorf("amount=0 should accrue nothing: Wear=%d", stall2.Wear)
	}

	// Seller owns no wearable stall: no panic, no-op. Drop the tag so the stall
	// no longer scopes in.
	w3, owner3, stall3 := stallTestWorld(1, 400, 600, 100)
	stall3.Tags = nil
	accrueStallWear(w3, owner3, producerSale(100), now)
	if stall3.Wear != 100 {
		t.Errorf("untagged stall should not wear: Wear=%d", stall3.Wear)
	}
}

// TestAccrueStallWear_NetMargin_Resale is the LLM-411 fix: a reseller's stall wears on
// what the sale EARNED him, not on what it turned over. Live case — the distributor
// buys milk at ~1 coin/unit and moves it on at ~1.33: under the old gross accrual his
// upkeep ran to ~75% of his entire margin and he ground down to 3 coins with empty
// shelves, stalling the farms → distributor → village pipe.
func TestAccrueStallWear_NetMargin_Resale(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()

	// Bought 6 milk for 6 coins (1/unit); sells 3 of them for 4. Margin = 4 − 3 = 1.
	w, owner, stall := stallTestWorld(1, 400, 600, 0)
	boughtAt(w, owner.ID, "milk", 6, 6, now.Add(-48*time.Hour))
	accrueStallWear(w, owner, saleWear{
		Lines:     []QuoteLine{{ItemKind: "milk", Qty: 3}},
		Consumers: 1,
		Amount:    4,
		Charge:    4,
	}, now)
	if stall.Wear != 1 {
		t.Errorf("Wear = %d, want 1 (4 coins taken, 3 of cost basis — the margin is what wears)", stall.Wear)
	}

	// Units are Qty × Consumers: a 2-consumer bundle of 3 each moves 6 units, so the
	// whole 6-coin cost basis is behind a 9-coin sale. Margin 3.
	w2, owner2, stall2 := stallTestWorld(1, 400, 600, 0)
	boughtAt(w2, owner2.ID, "milk", 6, 6, now.Add(-48*time.Hour))
	accrueStallWear(w2, owner2, saleWear{
		Lines:     []QuoteLine{{ItemKind: "milk", Qty: 3}},
		Consumers: 2,
		Amount:    9,
		Charge:    9,
	}, now)
	if stall2.Wear != 3 {
		t.Errorf("Wear = %d, want 3 (6 units × 1 coin of basis under a 9-coin sale)", stall2.Wear)
	}
}

// TestAccrueStallWear_ProducerUnaffected pins the property the whole fix rests on: a
// good the seller MADE or foraged has no purchase behind it, so it has no cost basis
// and its sale still wears on the full amount. Ezekiel's nails, Hannah's porridge, and
// the farms' produce are untouched by LLM-411 — which is why the thresholds did not
// need a village-wide recalibration. The seller's OWN sales of the item (he is the
// SellerID on those rows, not the BuyerID) must not be misread as purchases.
func TestAccrueStallWear_ProducerUnaffected(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	w, owner, stall := stallTestWorld(1, 400, 600, 0)
	w.SeedPriceBook([]PriceBookSeedRecord{{
		Key:         PriceBookKey{SellerID: owner.ID, Item: "nail"},
		Observation: PriceObservation{BuyerID: "villager", Amount: 20, Qty: 10, Consumers: 1, At: now.Add(-time.Hour)},
	}})
	accrueStallWear(w, owner, saleWear{
		Lines:     []QuoteLine{{ItemKind: "nail", Qty: 5}},
		Consumers: 1,
		Amount:    10,
		Charge:    10,
	}, now)
	if stall.Wear != 10 {
		t.Errorf("Wear = %d, want 10 — a producer has no cost basis, so the full amount wears", stall.Wear)
	}
}

// TestAccrueStallWear_ServiceFullAccrual: a service (nights_stay) has no goods behind
// it — the keeper never BUYS a night's lodging — so it has no cost basis and wears on
// the full amount, exactly as before LLM-411.
func TestAccrueStallWear_ServiceFullAccrual(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	w, owner, stall := stallTestWorld(1, 400, 600, 0)
	accrueStallWear(w, owner, saleWear{
		Lines:     []QuoteLine{{ItemKind: "nights_stay", Qty: 1}},
		Consumers: 1,
		Amount:    8,
		Charge:    8,
	}, now)
	if stall.Wear != 8 {
		t.Errorf("Wear = %d, want 8 (a service has no cost basis — full accrual)", stall.Wear)
	}
}

// TestAccrueStallWear_SaleAtOrBelowCost: a reseller moving goods at or under what they
// cost him earns nothing, so his shop doesn't grind down for the privilege. The
// max(0, …) arm of the formula.
func TestAccrueStallWear_SaleAtOrBelowCost(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	w, owner, stall := stallTestWorld(1, 400, 600, 30)
	boughtAt(w, owner.ID, "cheese", 4, 8, now.Add(-24*time.Hour)) // 2 coins/unit
	accrueStallWear(w, owner, saleWear{
		Lines:     []QuoteLine{{ItemKind: "cheese", Qty: 2}},
		Consumers: 1,
		Amount:    4, // sold at cost: 2 units × 2 coins
		Charge:    4,
	}, now)
	if stall.Wear != 30 {
		t.Errorf("Wear = %d, want 30 (sold at cost — no margin, no wear)", stall.Wear)
	}
}

// TestAccrueStallWear_PartialPaymentLegsSplitMargin: an LLM-357 partial-payment
// commission collects a deposit at accept and the balance at deliver_order — two legs,
// one sale. Each wears its FLOORED proportional share of the sale's margin, so the two
// together never exceed the margin (they may under-tax by one coin — the safe
// direction). The old half-up-both-legs math over-taxed by a coin, crossing the repair
// threshold early; this pins that it can't.
func TestAccrueStallWear_PartialPaymentLegsSplitMargin(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	w, owner, stall := stallTestWorld(1, 400, 600, 0)
	boughtAt(w, owner.ID, "flour", 10, 10, now.Add(-72*time.Hour)) // 1 coin/unit
	sale := func(charge int) saleWear {
		return saleWear{
			Lines:     []QuoteLine{{ItemKind: "flour", Qty: 10}},
			Consumers: 1,
			Amount:    20, // margin = 20 − 10 = 10
			Charge:    charge,
		}
	}
	accrueStallWear(w, owner, sale(5), now)  // deposit: floor(5/20 × 10) = 2
	accrueStallWear(w, owner, sale(15), now) // balance: floor(15/20 × 10) = 7
	if stall.Wear != 9 {
		t.Errorf("Wear = %d, want 9 — floored deposit (2) + balance (7); the two legs never exceed the 10-coin margin", stall.Wear)
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
	accrueStallWear(w, owner, producerSale(math.MaxInt), now) // 500 + MaxInt*1 overflows int
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
	accrueStallWear(w, owner, producerSale(100), now)   // would add 200 without the freeze
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
