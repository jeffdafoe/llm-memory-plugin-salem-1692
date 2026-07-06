package sim

import (
	"testing"
	"time"
)

// restock_tick_test.go — ZBBS-WORK-322, the buy-side restock producer. Covers
// the reorder-threshold math, the BuyEntries filter, the EvaluateRestock
// stamping decision (who warrants), the eligibility gate (scope + rest + open-
// cycle), and the pct=0 / cap=0 off cases. Reuses sleepTestWorld / intptr from
// npc_sleep_test.go and warrantKinds / hasWarrantKind (same package).

// reseller builds an agent NPC holding a single `buy` entry for `item` at the
// given cap, with `onHand` units in inventory. No schedule — the producer does
// not gate on shift (a low shelf is low whether or not the keeper is "on").
func reseller(id ActorID, kind ActorKind, item ItemKind, cap, onHand int) *Actor {
	return &Actor{
		ID:        id,
		Kind:      kind,
		LLMAgent:  string(id) + "-agent",
		Inventory: map[ItemKind]int{item: onHand},
		RestockPolicy: &RestockPolicy{Restock: []RestockEntry{
			{Item: item, Source: RestockSourceBuy, Max: cap},
		}},
	}
}

// restockWorld is a sleepTestWorld with the reorder threshold set to 25%.
func restockWorld(actors ...*Actor) *World {
	w := sleepTestWorld(actors...)
	w.Settings.RestockReorderPct = DefaultRestockReorderPct // 25
	return w
}

// addSupplier wires a first-hand supplier of item into the world: an NPC
// stationed at its own workplace structure, holding stock, with a `produce`
// entry for the item so it passes the LLM-252 supplier gate. Buy warrants are
// gated on an actionable buy path (LLM-260), so tests exercising a different
// gate add one of these to keep the path open.
func addSupplier(w *World, id ActorID, item ItemKind) *Actor {
	sid := StructureID("shop-" + string(id))
	v := &Actor{
		ID:              id,
		Kind:            KindNPCStateful,
		DisplayName:     string(id),
		WorkStructureID: sid,
		Inventory:       map[ItemKind]int{item: 10},
		RestockPolicy: &RestockPolicy{Restock: []RestockEntry{
			{Item: item, Source: RestockSourceProduce, Max: 20},
		}},
	}
	w.Actors[id] = v
	if w.Structures == nil {
		w.Structures = map[StructureID]*Structure{}
	}
	w.Structures[sid] = &Structure{}
	return v
}

// forageBushObj builds an owned forage-to-sell bush: a finite, yield-only
// (Amount 0) gather row for item with `avail` ripe units.
func forageBushObj(owner ActorID, item ItemKind, avail int) *VillageObject {
	q := avail
	return &VillageObject{
		OwnerActorID: owner,
		Refreshes: []*ObjectRefresh{
			{Attribute: "hunger", Amount: 0, GatherItem: item, AvailableQuantity: &q},
		},
	}
}

// rememberForageBush marks bushID as a remembered gather:<item> source on the
// actor's known-places — what LLM-77 ownership-seeding records for an owner's own
// bushes, and the precondition the forage warrant's actionability gate reads.
func rememberForageBush(a *Actor, item ItemKind, bushID VillageObjectID) {
	if a.KnownPlaces == nil {
		a.KnownPlaces = map[PlaceRef]*KnownPlace{}
	}
	a.KnownPlaces[PlaceRef(bushID)] = &KnownPlace{
		Ref:         PlaceRef(bushID),
		Kind:        PlaceKindObject,
		Affordances: []string{"gather:" + string(item)},
	}
}

func TestRestockReorderThresholdMet(t *testing.T) {
	cases := []struct {
		current, cap, pct, floor int
		want                     bool
	}{
		// Pure cap-fraction goods (floor 0) — unchanged behavior.
		{0, 10, 25, 0, true},    // empty shelf
		{2, 10, 25, 0, true},    // 20% < 25%
		{3, 10, 25, 0, false},   // 30% >= 25%
		{2, 8, 25, 0, false},    // 25% exactly is NOT below threshold (strict <)
		{1, 8, 25, 0, true},     // 12.5% < 25%
		{5, 10, 0, 0, false},    // pct 0 disables
		{0, 0, 25, 0, false},    // cap 0, no floor — nothing to reorder against
		{0, 10, 0, 0, false},    // both off
		{100, 10, 25, 0, false}, // over cap
		// Sub-one-unit fraction (cap*pct < 100) rounds up: reorder at the last
		// unit, not only when empty. cap 2 @ 25% = 0.5 (the skillet case, LLM-82).
		{0, 2, 25, 0, true},  // empty
		{1, 2, 25, 0, true},  // down to the last unit — now reorders (was the bug: fired only at 0)
		{2, 2, 25, 0, false}, // at full cap — does not reorder
		{1, 3, 25, 0, true},  // cap 3 @ 25% = 0.75, also sub-one-unit → fires at the last unit

		// Produce-input batch floor (LLM-279). Hannah's water: batch 5, derived
		// cap 10 → floor 10 (2 batches). The fraction alone fires only at <=2.
		{10, 10, 25, 10, false}, // two full batches on hand — not low
		{5, 10, 25, 10, true},   // one batch left — reorders NOW (mode 1: fires before the stall, cap fraction would not: 500>=250)
		{4, 10, 25, 10, true},   // knocked off the batch multiple (mode 2 deadlock: fraction 400>=250 would strand it forever)
		{9, 10, 25, 10, true},   // just under two batches
		// Floor is absolute below pct==0 — the off-switch disables it too.
		{4, 10, 0, 10, false},
		// Floor fires even with no cap configured (explicit buy input, Max/Target unset).
		{4, 0, 25, 10, true},
		{10, 0, 25, 10, false}, // at/above the floor, no cap → not low
		// A small floor that doesn't fire still leaves the cap fraction in force.
		{2, 10, 25, 1, true}, // 2 >= floor 1, but 20% < 25% → low by fraction
	}
	for _, c := range cases {
		if got := RestockReorderThresholdMet(c.current, c.cap, c.pct, c.floor); got != c.want {
			t.Errorf("RestockReorderThresholdMet(cur=%d,cap=%d,pct=%d,floor=%d) = %v, want %v",
				c.current, c.cap, c.pct, c.floor, got, c.want)
		}
	}
}

func TestRestockPolicyBuyEntriesFilters(t *testing.T) {
	p := &RestockPolicy{Restock: []RestockEntry{
		{Item: "bread", Source: RestockSourceProduce},
		{Item: "ale", Source: RestockSourceBuy},
		{Item: "stew", Source: RestockSourceProduce},
		{Item: "salt", Source: RestockSourceBuy},
	}}
	got := p.BuyEntries()
	if len(got) != 2 {
		t.Fatalf("BuyEntries count = %d, want 2", len(got))
	}
	if got[0].Item != "ale" || got[1].Item != "salt" {
		t.Errorf("BuyEntries = %+v, want [ale, salt]", got)
	}
	// nil policy is safe.
	var nilp *RestockPolicy
	if nilp.BuyEntries() != nil {
		t.Error("nil policy BuyEntries should be nil")
	}
}

// TestEvaluateRestock_LowStockStamps: a reseller below the reorder threshold
// gets a restock warrant carrying the low item.
func TestEvaluateRestock_LowStockStamps(t *testing.T) {
	a := reseller("merchant", KindNPCStateful, "ale", 20, 3) // 15% < 25%
	w := restockWorld(a)
	addSupplier(w, "brewer", "ale")
	now := time.Now().UTC()

	res, err := EvaluateRestock(now).Fn(w)
	if err != nil {
		t.Fatalf("EvaluateRestock: %v", err)
	}
	if res.(int) != 1 {
		t.Errorf("stamped = %d, want 1", res.(int))
	}
	if !hasWarrantKind(a, WarrantKindRestock) {
		t.Fatalf("expected a restock warrant; kinds = %v", warrantKinds(a))
	}
	// Carries the representative low item.
	var found bool
	for _, m := range a.Warrants {
		if r, ok := m.Reason.(RestockWarrantReason); ok {
			found = true
			if r.Item != "ale" {
				t.Errorf("warrant item = %q, want ale", r.Item)
			}
		}
	}
	if !found {
		t.Error("no RestockWarrantReason on the actor")
	}
}

// TestEvaluateRestock_LowForageStockStamps: a grower whose own HARVESTED sell-
// stock runs low warrants restock the same way a buy-side reseller does (LLM-90),
// with the forage Source so the cue line routes to "## Your bushes to harvest".
// The forage warrant is gated on an ACTIONABLE bush — a remembered, still-owned
// forage source for the item — so here she remembers her own raspberry bush.
func TestEvaluateRestock_LowForageStockStamps(t *testing.T) {
	a := &Actor{
		ID:        "prudence",
		Kind:      KindNPCStateful,
		LLMAgent:  "prudence-agent",
		Inventory: map[ItemKind]int{"raspberries": 1}, // 10% of cap 10 < 25%
		RestockPolicy: &RestockPolicy{Restock: []RestockEntry{
			{Item: "raspberries", Source: RestockSourceForage, Max: 10},
		}},
	}
	rememberForageBush(a, "raspberries", "bushA")
	w := restockWorld(a)
	w.VillageObjects = map[VillageObjectID]*VillageObject{
		"bushA": forageBushObj("prudence", "raspberries", 10),
	}
	if res, _ := EvaluateRestock(time.Now().UTC()).Fn(w); res.(int) != 1 {
		t.Fatalf("stamped = %d for a low forage entry, want 1", res.(int))
	}
	var found bool
	for _, m := range a.Warrants {
		if r, ok := m.Reason.(RestockWarrantReason); ok {
			found = true
			if r.Item != "raspberries" || r.Source != RestockSourceForage {
				t.Errorf("warrant = {%q, %q}, want {raspberries, forage}", r.Item, r.Source)
			}
		}
	}
	if !found {
		t.Error("no RestockWarrantReason on the grower")
	}
}

// TestEvaluateRestock_ConservingKeeperNoBuyWarrant: LLM-298 — a conserving keeper
// (coin-poor + overstocked, sim.actorConserving) is told by "## Restocking" to hold off
// buying and sell down, so the buy-restock wakeup is suppressed rather than re-firing
// every minute for a keeper with correctly nothing to do (the live John Ellis carrots
// nag). The floor-off control proves the conserve gate is what suppresses it.
func TestEvaluateRestock_ConservingKeeperNoBuyWarrant(t *testing.T) {
	keeper := &Actor{
		ID:        "john",
		Kind:      KindNPCStateful,
		LLMAgent:  "john-agent",
		Coins:     8,                                         // below the floor
		Inventory: map[ItemKind]int{"ale": 20, "carrots": 1}, // ale overstocked (produce ware), carrots low (buy)
		RestockPolicy: &RestockPolicy{Restock: []RestockEntry{
			{Item: "ale", Source: RestockSourceProduce, Max: 24},
			{Item: "carrots", Source: RestockSourceBuy, Max: 6},
		}},
	}
	w := restockWorld(keeper)
	w.Settings.MerchantCoinFloor = 10
	addSupplier(w, "grocer", "carrots") // an open buy path — the warrant is otherwise gated on one
	if res, _ := EvaluateRestock(time.Now().UTC()).Fn(w); res.(int) != 0 {
		t.Errorf("stamped = %d for a conserving keeper, want 0 (buy wakeup suppressed)", res.(int))
	}
	if hasWarrantKind(keeper, WarrantKindRestock) {
		t.Errorf("conserving keeper got a restock warrant; kinds = %v", warrantKinds(keeper))
	}
	// Control: same keeper with the floor disabled → not conserving → the low buy warrants.
	keeper.Warrants = nil
	keeper.WarrantedSince = nil
	w.Settings.MerchantCoinFloor = 0
	if res, _ := EvaluateRestock(time.Now().UTC()).Fn(w); res.(int) != 1 {
		t.Errorf("stamped = %d with the floor off, want 1 (the conserve gate is what suppresses)", res.(int))
	}
}

// TestEvaluateRestock_CoinPoorEmptyShelfStillWarrants: LLM-298 — the conserve gate is
// coin-poor AND overstocked. A coin-poor keeper with EMPTY shelves (nothing overstocked)
// is NOT conserving — it still needs to buy inputs to have anything to sell — so the buy
// warrant stands (the empty-shelf exception, mirroring merchantConserve).
func TestEvaluateRestock_CoinPoorEmptyShelfStillWarrants(t *testing.T) {
	keeper := &Actor{
		ID:        "john",
		Kind:      KindNPCStateful,
		LLMAgent:  "john-agent",
		Coins:     8, // below the floor, but nothing overstocked
		Inventory: map[ItemKind]int{"carrots": 1},
		RestockPolicy: &RestockPolicy{Restock: []RestockEntry{
			{Item: "carrots", Source: RestockSourceBuy, Max: 6},
		}},
	}
	w := restockWorld(keeper)
	w.Settings.MerchantCoinFloor = 10
	addSupplier(w, "grocer", "carrots")
	if res, _ := EvaluateRestock(time.Now().UTC()).Fn(w); res.(int) != 1 {
		t.Errorf("stamped = %d for a coin-poor but empty-shelf keeper, want 1 (empty-shelf exception)", res.(int))
	}
}

// TestEvaluateRestock_ConservingKeeperForageStillWarrants: LLM-298 — conserve is a COIN
// gate (don't spend coin buying). Harvesting one's own bushes costs no coin, so a
// conserving keeper's low FORAGE entry still wakes it to restock — only the buy wakeup is
// suppressed.
func TestEvaluateRestock_ConservingKeeperForageStillWarrants(t *testing.T) {
	keeper := &Actor{
		ID:        "prudence",
		Kind:      KindNPCStateful,
		LLMAgent:  "prudence-agent",
		Coins:     8,                                                  // below the floor
		Inventory: map[ItemKind]int{"porridge": 20, "raspberries": 1}, // porridge overstocked (produce), raspberries low (forage)
		RestockPolicy: &RestockPolicy{Restock: []RestockEntry{
			{Item: "porridge", Source: RestockSourceProduce, Max: 30},
			{Item: "raspberries", Source: RestockSourceForage, Max: 10},
		}},
	}
	rememberForageBush(keeper, "raspberries", "bushA")
	w := restockWorld(keeper)
	w.Settings.MerchantCoinFloor = 10
	w.VillageObjects = map[VillageObjectID]*VillageObject{
		"bushA": forageBushObj("prudence", "raspberries", 10),
	}
	if res, _ := EvaluateRestock(time.Now().UTC()).Fn(w); res.(int) != 1 {
		t.Errorf("stamped = %d for a conserving forager, want 1 (forage wakeup is free, not suppressed)", res.(int))
	}
	for _, m := range keeper.Warrants {
		if r, ok := m.Reason.(RestockWarrantReason); ok && r.Source != RestockSourceForage {
			t.Errorf("warrant source = %q, want forage", r.Source)
		}
	}
}

// TestEvaluateRestock_ForageNoRememberedBush_NoStamp: the actionability gate
// (code_review LLM-90). A low forage entry with NO remembered, still-owned bush
// for the item must NOT warrant — buildForage would render no "## Your bushes to
// harvest" section, so a high-information warrant (bypasses noop-skip) would wake
// the grower every scan pointing at a section that isn't there (a wake loop on
// forgotten / sold / deleted / never-seeded bushes).
func TestEvaluateRestock_ForageNoRememberedBush_NoStamp(t *testing.T) {
	a := &Actor{
		ID:        "prudence",
		Kind:      KindNPCStateful,
		LLMAgent:  "prudence-agent",
		Inventory: map[ItemKind]int{"raspberries": 0}, // empty, below threshold
		RestockPolicy: &RestockPolicy{Restock: []RestockEntry{
			{Item: "raspberries", Source: RestockSourceForage, Max: 10},
		}},
	}
	// No KnownPlaces, no village objects — she remembers no bush.
	w := restockWorld(a)
	if res, _ := EvaluateRestock(time.Now().UTC()).Fn(w); res.(int) != 0 {
		t.Fatalf("stamped = %d for a low forage entry with no actionable bush, want 0", res.(int))
	}
	if hasWarrantKind(a, WarrantKindRestock) {
		t.Error("a forage entry with no remembered bush must not warrant (wake-loop guard)")
	}
}

// TestEvaluateRestock_AtThresholdNoStamp: stock at/above the threshold → no warrant.
func TestEvaluateRestock_AtThresholdNoStamp(t *testing.T) {
	a := reseller("merchant", KindNPCStateful, "ale", 20, 5) // 25% — not below
	w := restockWorld(a)
	addSupplier(w, "brewer", "ale")
	if res, _ := EvaluateRestock(time.Now().UTC()).Fn(w); res.(int) != 0 {
		t.Errorf("stamped = %d at threshold, want 0", res.(int))
	}
	if hasWarrantKind(a, WarrantKindRestock) {
		t.Error("at-threshold reseller should not warrant")
	}
}

// TestEvaluateRestock_DisabledByPctZero: RestockReorderPct=0 disables the producer.
func TestEvaluateRestock_DisabledByPctZero(t *testing.T) {
	a := reseller("merchant", KindNPCStateful, "ale", 20, 0) // empty shelf
	w := restockWorld(a)
	addSupplier(w, "brewer", "ale")
	w.Settings.RestockReorderPct = 0
	if res, _ := EvaluateRestock(time.Now().UTC()).Fn(w); res.(int) != 0 {
		t.Errorf("stamped = %d with pct=0, want 0 (disabled)", res.(int))
	}
	if hasWarrantKind(a, WarrantKindRestock) {
		t.Error("pct=0 should disable the producer entirely")
	}
}

// TestEvaluateRestock_CapZeroSkipped: a buy entry with no cap can't be measured
// as a fraction, so it's skipped.
func TestEvaluateRestock_CapZeroSkipped(t *testing.T) {
	a := reseller("merchant", KindNPCStateful, "ale", 0, 0) // no cap configured
	w := restockWorld(a)
	addSupplier(w, "brewer", "ale")
	if res, _ := EvaluateRestock(time.Now().UTC()).Fn(w); res.(int) != 0 {
		t.Errorf("stamped = %d with cap=0, want 0", res.(int))
	}
}

// TestEvaluateRestock_ProduceEntriesIgnored: a low PRODUCE entry must not warrant
// restock — that's the produce tick's job, not this producer's.
func TestEvaluateRestock_ProduceEntriesIgnored(t *testing.T) {
	a := &Actor{
		ID:        "baker",
		Kind:      KindNPCStateful,
		LLMAgent:  "baker-agent",
		Inventory: map[ItemKind]int{"bread": 0},
		RestockPolicy: &RestockPolicy{Restock: []RestockEntry{
			{Item: "bread", Source: RestockSourceProduce, Max: 20},
		}},
	}
	w := restockWorld(a)
	if res, _ := EvaluateRestock(time.Now().UTC()).Fn(w); res.(int) != 0 {
		t.Errorf("stamped = %d for produce entry, want 0", res.(int))
	}
}

// TestEvaluateRestock_ScopeExclusions: PCs, transient visitors, and decoratives
// are out of scope even with empty buy shelves.
func TestEvaluateRestock_ScopeExclusions(t *testing.T) {
	pc := reseller("p", KindPC, "ale", 20, 0)
	dec := reseller("d", KindDecorative, "ale", 20, 0)
	vis := reseller("v", KindNPCShared, "ale", 20, 0)
	vis.VisitorState = &VisitorState{Archetype: "traveler", ExpiresAt: time.Now().Add(time.Hour)}
	w := restockWorld(pc, dec, vis)
	addSupplier(w, "brewer", "ale")

	if res, _ := EvaluateRestock(time.Now().UTC()).Fn(w); res.(int) != 0 {
		t.Errorf("stamped = %d, want 0 (all out of scope)", res.(int))
	}
	for _, a := range []*Actor{pc, dec, vis} {
		if hasWarrantKind(a, WarrantKindRestock) {
			t.Errorf("%s (kind %v) should be out of scope", a.ID, a.Kind)
		}
	}
}

// TestEvaluateRestock_SharedVAReseller: a shared-VA NPC (KindNPCShared, no
// visitor state) IS in scope — shared vendors restock too.
func TestEvaluateRestock_SharedVAReseller(t *testing.T) {
	a := reseller("vendor", KindNPCShared, "salt", 20, 1)
	w := restockWorld(a)
	addSupplier(w, "salter", "salt")
	if res, _ := EvaluateRestock(time.Now().UTC()).Fn(w); res.(int) != 1 {
		t.Errorf("stamped = %d, want 1 (shared VA in scope)", res.(int))
	}
}

// TestEvaluateRestock_RestingAndOpenCycleSkipped: sleeping, on-break, already-
// warranted, and mid-tick resellers are all skipped (the shared suppressor).
func TestEvaluateRestock_RestingAndOpenCycleSkipped(t *testing.T) {
	now := time.Now().UTC()
	future := now.Add(time.Hour)
	since := now.Add(-time.Minute)

	sleeping := reseller("s", KindNPCStateful, "ale", 20, 0)
	sleeping.SleepingUntil = &future

	onBreak := reseller("b", KindNPCStateful, "ale", 20, 0)
	onBreak.BreakUntil = &future

	warranted := reseller("w", KindNPCStateful, "ale", 20, 0)
	warranted.WarrantedSince = &since

	inFlight := reseller("f", KindNPCStateful, "ale", 20, 0)
	inFlight.TickInFlight = true

	world := restockWorld(sleeping, onBreak, warranted, inFlight)
	addSupplier(world, "brewer", "ale")
	if res, _ := EvaluateRestock(now).Fn(world); res.(int) != 0 {
		t.Errorf("stamped = %d, want 0 (all suppressed)", res.(int))
	}
	if hasWarrantKind(sleeping, WarrantKindRestock) {
		t.Error("sleeping reseller should be skipped")
	}
	if hasWarrantKind(onBreak, WarrantKindRestock) {
		t.Error("on-break reseller should be skipped")
	}
	if hasWarrantKind(inFlight, WarrantKindRestock) {
		t.Error("mid-tick reseller should be skipped")
	}
}

// TestEvaluateRestock_WalkingSkipped: a reseller below the reorder threshold but
// already en route somewhere (a live MoveIntent) is NOT re-warranted — the
// per-minute, level-triggered producer would otherwise thrash the multi-minute
// supplier trip, waking the reseller mid-walk to re-decide and reverse course
// (the Josiah-Thorne oscillation, ZBBS-HOME-386). Once it arrives and stops, the
// standing low-stock condition re-stamps it at the supplier.
func TestEvaluateRestock_WalkingSkipped(t *testing.T) {
	now := time.Now().UTC()

	walking := reseller("walker", KindNPCStateful, "ale", 20, 0) // empty shelf — would warrant if stationary
	walking.MoveIntent = &MoveIntent{
		Destination: NewStructureEnterDestination("ellis-farm"),
		AttemptID:   1,
	}

	w := restockWorld(walking)
	addSupplier(w, "brewer", "ale")
	if res, _ := EvaluateRestock(now).Fn(w); res.(int) != 0 {
		t.Errorf("stamped = %d, want 0 (walking reseller left to arrive)", res.(int))
	}
	if hasWarrantKind(walking, WarrantKindRestock) {
		t.Error("a reseller mid-walk should not be re-warranted for restock")
	}

	// Arrived (MoveIntent cleared): the same standing low shelf now re-stamps,
	// so the trip-then-restock cycle still completes — the gate only defers, it
	// doesn't drop the restock.
	walking.MoveIntent = nil
	if res, _ := EvaluateRestock(now).Fn(w); res.(int) != 1 {
		t.Errorf("stamped = %d after arrival, want 1", res.(int))
	}
	if !hasWarrantKind(walking, WarrantKindRestock) {
		t.Error("a stationary low-stock reseller should warrant after arriving")
	}
}

// TestFirstActionableLowEntry_BuyDeterministicOrder: the first low buy entry in
// policy order with an actionable buy path (LLM-260) is the one chosen, entries
// above threshold are passed over, and the buy source is reported.
func TestFirstActionableLowEntry_BuyDeterministicOrder(t *testing.T) {
	a := &Actor{
		ID:        "merchant",
		Inventory: map[ItemKind]int{"flour": 9, "salt": 1, "ale": 0},
		RestockPolicy: &RestockPolicy{Restock: []RestockEntry{
			{Item: "flour", Source: RestockSourceBuy, Max: 10}, // stocked, above threshold
			{Item: "salt", Source: RestockSourceBuy, Max: 10},  // low
			{Item: "ale", Source: RestockSourceBuy, Max: 10},   // also low, but later
		}},
	}
	w := restockWorld(a)
	addSupplier(w, "salter", "salt")
	addSupplier(w, "brewer", "ale")
	if e, src, ok := firstActionableLowEntry(a, w, 25, time.Now().UTC(), false); !ok || e.Item != "salt" || src != RestockSourceBuy {
		t.Errorf("first actionable low = (%q, %q, %v), want (salt, buy, true)", e.Item, src, ok)
	}
	// The low buy entry with NO buy path is passed over in favor of a later one
	// that has a supplier (the LLM-260 buy actionability gate).
	delete(w.Actors, "salter")
	if e, _, ok := firstActionableLowEntry(a, w, 25, time.Now().UTC(), false); !ok || e.Item != "ale" {
		t.Errorf("with salt unsourced, first actionable low = (%q, %v), want (ale, true)", e.Item, ok)
	}
}

// TestFirstActionableLowEntry_BuyBeforeForageAndActionability: buy is chosen
// before forage (LLM-90 keeps the buy-side reseller's representative Item
// unchanged); a forage-only low reports the forage source ONLY when an actionable
// bush is remembered, and is skipped otherwise (the wake-loop guard).
func TestFirstActionableLowEntry_BuyBeforeForageAndActionability(t *testing.T) {
	bushWorld := func(owner ActorID) map[VillageObjectID]*VillageObject {
		return map[VillageObjectID]*VillageObject{"bushA": forageBushObj(owner, "raspberries", 10)}
	}

	// Both a low buy and a low (actionable) forage entry → buy wins.
	both := &Actor{
		ID:        "prudence",
		Inventory: map[ItemKind]int{"raspberries": 0, "milk": 1},
		RestockPolicy: &RestockPolicy{Restock: []RestockEntry{
			{Item: "raspberries", Source: RestockSourceForage, Max: 10},
			{Item: "milk", Source: RestockSourceBuy, Max: 10},
		}},
	}
	rememberForageBush(both, "raspberries", "bushA")
	wBoth := restockWorld(both)
	wBoth.VillageObjects = bushWorld("prudence")
	addSupplier(wBoth, "dairy", "milk")
	if e, src, ok := firstActionableLowEntry(both, wBoth, 25, time.Now().UTC(), false); !ok || e.Item != "milk" || src != RestockSourceBuy {
		t.Errorf("mixed low set: got (%q, %q, %v), want (milk, buy, true)", e.Item, src, ok)
	}

	// Forage-only low WITH an actionable remembered bush → forage source returned.
	forageOnly := &Actor{
		ID:        "prudence",
		Inventory: map[ItemKind]int{"milk": 9, "raspberries": 1},
		RestockPolicy: &RestockPolicy{Restock: []RestockEntry{
			{Item: "milk", Source: RestockSourceBuy, Max: 10},           // stocked
			{Item: "raspberries", Source: RestockSourceForage, Max: 10}, // low
		}},
	}
	rememberForageBush(forageOnly, "raspberries", "bushA")
	wForage := restockWorld(forageOnly)
	wForage.VillageObjects = bushWorld("prudence")
	if e, src, ok := firstActionableLowEntry(forageOnly, wForage, 25, time.Now().UTC(), false); !ok || e.Item != "raspberries" || src != RestockSourceForage {
		t.Errorf("forage-only low: got (%q, %q, %v), want (raspberries, forage, true)", e.Item, src, ok)
	}

	// Forage-only low WITHOUT a remembered bush → not actionable, no entry.
	noBush := &Actor{
		ID:        "prudence",
		Inventory: map[ItemKind]int{"raspberries": 1},
		RestockPolicy: &RestockPolicy{Restock: []RestockEntry{
			{Item: "raspberries", Source: RestockSourceForage, Max: 10},
		}},
	}
	if _, _, ok := firstActionableLowEntry(noBush, restockWorld(noBush), 25, time.Now().UTC(), false); ok {
		t.Error("a low forage entry with no remembered bush must not be actionable")
	}
}

// TestEvaluateRestock_BuyNoVendorNoStamp: the LLM-260 buy-side actionability
// gate (the wake-loop guard, mirroring the forage bush gate). A low buy entry
// with NO qualifying vendor anywhere must NOT warrant — buildRestocking omits
// the item (LLM-216), so a warrant would wake the reseller every scan pointing
// at a section that isn't there.
func TestEvaluateRestock_BuyNoVendorNoStamp(t *testing.T) {
	a := reseller("merchant", KindNPCStateful, "ale", 20, 0) // empty shelf
	w := restockWorld(a)                                     // no supplier in the world
	if res, _ := EvaluateRestock(time.Now().UTC()).Fn(w); res.(int) != 0 {
		t.Fatalf("stamped = %d for a low buy entry with no vendor, want 0", res.(int))
	}
	if hasWarrantKind(a, WarrantKindRestock) {
		t.Error("a buy entry with no vendor must not warrant (wake-loop guard)")
	}
}

// TestEvaluateRestock_DerivedInputStamps: derived procurement demand (LLM-260).
// A producer with a `produce` entry whose recipe consumes inputs it neither
// self-sources nor explicitly buys (the live Hannah Boggs porridge case) is
// woken to buy the missing input once a vendor for it exists — no hand-authored
// `buy` entry required.
func TestEvaluateRestock_DerivedInputStamps(t *testing.T) {
	a := &Actor{
		ID:        "hannah",
		Kind:      KindNPCStateful,
		LLMAgent:  "hannah-agent",
		Inventory: map[ItemKind]int{"porridge": 11}, // output stocked; inputs at zero
		RestockPolicy: &RestockPolicy{Restock: []RestockEntry{
			{Item: "porridge", Source: RestockSourceProduce, Max: 12},
		}},
	}
	w := restockWorld(a)
	w.Recipes = map[ItemKind]*ItemRecipe{
		"porridge": {OutputItem: "porridge", OutputQty: 4, RateQty: 4, RatePerHours: 1,
			Inputs: []RecipeInput{{Item: "milk", Qty: 3}, {Item: "water", Qty: 5}}},
	}
	addSupplier(w, "dairy", "milk") // milk obtainable; water has no vendor

	if res, _ := EvaluateRestock(time.Now().UTC()).Fn(w); res.(int) != 1 {
		t.Fatalf("stamped = %d for a derived low input, want 1", res.(int))
	}
	var found bool
	for _, m := range a.Warrants {
		if r, ok := m.Reason.(RestockWarrantReason); ok {
			found = true
			if r.Item != "milk" || r.Source != RestockSourceBuy {
				t.Errorf("warrant = {%q, %q}, want {milk, buy} (water has no vendor)", r.Item, r.Source)
			}
		}
	}
	if !found {
		t.Error("no RestockWarrantReason on the producer")
	}
}

// TestEvaluateRestock_InputBatchFloorStamps: LLM-279. A producer's recipe input
// stranded in the deadlock band — above the cap fraction but below one full batch —
// is now woken to reorder. Hannah's porridge draws milk 3 per batch (derived cap 9,
// cap fraction ~2.25). Milk pinned at 4 sits ABOVE that fraction, so the old rule
// left her stranded (she can't cover a 3-milk batch yet was never reordered), but
// BELOW the 2×batch floor of 6 — so the warrant now stamps. This is the permanent-
// deadlock case (failure mode 2) from the ticket, and the reason it's the headline
// fix: without the floor the producer never gets woken at all.
func TestEvaluateRestock_InputBatchFloorStamps(t *testing.T) {
	a := &Actor{
		ID:        "hannah",
		Kind:      KindNPCStateful,
		LLMAgent:  "hannah-agent",
		Inventory: map[ItemKind]int{"porridge": 11, "milk": 4, "water": 20},
		RestockPolicy: &RestockPolicy{Restock: []RestockEntry{
			{Item: "porridge", Source: RestockSourceProduce, Max: 12},
		}},
	}
	w := restockWorld(a)
	w.Recipes = map[ItemKind]*ItemRecipe{
		"porridge": {OutputItem: "porridge", OutputQty: 4, RateQty: 4, RatePerHours: 1,
			Inputs: []RecipeInput{{Item: "milk", Qty: 3}, {Item: "water", Qty: 5}}},
	}
	addSupplier(w, "dairy", "milk") // milk obtainable so the warrant is actionable

	if res, _ := EvaluateRestock(time.Now().UTC()).Fn(w); res.(int) != 1 {
		t.Fatalf("stamped = %d for milk below the batch floor (4 < 6), want 1", res.(int))
	}
	if !hasWarrantKind(a, WarrantKindRestock) {
		t.Error("no restock warrant for an input stranded above the cap fraction but below one batch")
	}
}

// TestEvaluateRestock_SelfSourcedInputNoDerivedDemand: a recipe input the actor
// self-sources (its own produce/forage entry — John Ellis's stew water) derives
// no buy demand, even with a vendor selling it and the input at zero.
func TestEvaluateRestock_SelfSourcedInputNoDerivedDemand(t *testing.T) {
	a := &Actor{
		ID:        "john",
		Kind:      KindNPCStateful,
		LLMAgent:  "john-agent",
		Inventory: map[ItemKind]int{"stew": 5, "water": 0},
		RestockPolicy: &RestockPolicy{Restock: []RestockEntry{
			{Item: "stew", Source: RestockSourceProduce, Max: 10},
			{Item: "water", Source: RestockSourceProduce, Max: 20},
		}},
	}
	w := restockWorld(a)
	w.Recipes = map[ItemKind]*ItemRecipe{
		"stew":  {OutputItem: "stew", OutputQty: 5, RateQty: 5, RatePerHours: 1, Inputs: []RecipeInput{{Item: "water", Qty: 10}}},
		"water": {OutputItem: "water", OutputQty: 10, RateQty: 10, RatePerHours: 1},
	}
	addSupplier(w, "drawer", "water")

	if res, _ := EvaluateRestock(time.Now().UTC()).Fn(w); res.(int) != 0 {
		t.Errorf("stamped = %d for a self-sourced input, want 0", res.(int))
	}
	if hasWarrantKind(a, WarrantKindRestock) {
		t.Error("a self-sourced recipe input must not derive buy demand")
	}
}

// TestActorHasBuyPath_Gates: the warrant-side mirror of the buildRestocking
// vendor drops — LLM-252 first-hand-supplier gate, LLM-216 remembered-shut and
// known-price-affordability drops, and the co-present bypass.
func TestActorHasBuyPath_Gates(t *testing.T) {
	now := time.Now().UTC()
	buyer := reseller("buyer", KindNPCStateful, "ale", 20, 0)

	// A vendor holding the item only via a past `buy` (a fellow reseller) is not
	// a supplier (LLM-252) — no path.
	w := restockWorld(buyer)
	v := addSupplier(w, "brewer", "ale")
	v.RestockPolicy = &RestockPolicy{Restock: []RestockEntry{{Item: "ale", Source: RestockSourceBuy, Max: 20}}}
	if actorHasBuyPath(w, buyer, "ale", now) {
		t.Error("a reseller's retail stock must not be a buy path (LLM-252)")
	}

	// First-hand supplier restored — path exists.
	v.RestockPolicy = &RestockPolicy{Restock: []RestockEntry{{Item: "ale", Source: RestockSourceProduce, Max: 20}}}
	if !actorHasBuyPath(w, buyer, "ale", now) {
		t.Fatal("a stocked first-hand supplier should be a buy path")
	}

	// Remembered shut → dropped as a walk-to destination (LLM-216).
	buyer.Observed.Observe(ObservedStateKey{StructureID: v.WorkStructureID, Condition: ObservedClosed}, now)
	if actorHasBuyPath(w, buyer, "ale", now) {
		t.Error("a supplier remembered shut must not be a buy path")
	}

	// ...unless the seller is co-present in the buyer's huddle — pay_with_item
	// resolves this very tick, walk-to drops don't apply.
	buyer.CurrentHuddleID = "h1"
	v.CurrentHuddleID = "h1"
	if !actorHasBuyPath(w, buyer, "ale", now) {
		t.Error("a co-present seller is a buy path regardless of the shut memory")
	}
	buyer.CurrentHuddleID = ""
	v.CurrentHuddleID = ""
	buyer.Observed.Clear(ObservedStateKey{StructureID: v.WorkStructureID, Condition: ObservedClosed})

	// A remembered price above the purse → dropped (LLM-216); an unknown price
	// is kept (patronage earns the number).
	buyer.Coins = 2
	w.PriceBook = map[PriceBookKey]*RingBuffer[PriceObservation]{}
	buf := NewRingBuffer[PriceObservation](4)
	buf.Push(PriceObservation{BuyerID: buyer.ID, Amount: 5, Qty: 1, At: now})
	w.PriceBook[PriceBookKey{SellerID: v.ID, Item: "ale"}] = buf
	if actorHasBuyPath(w, buyer, "ale", now) {
		t.Error("a supplier at a remembered price above the purse must not be a buy path")
	}
	buyer.Coins = 5
	if !actorHasBuyPath(w, buyer, "ale", now) {
		t.Error("a supplier at a remembered, affordable price should be a buy path")
	}
}
