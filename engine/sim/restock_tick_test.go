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
		current, cap, pct int
		want              bool
	}{
		{0, 10, 25, true},    // empty shelf
		{2, 10, 25, true},    // 20% < 25%
		{3, 10, 25, false},   // 30% >= 25%
		{2, 8, 25, false},    // 25% exactly is NOT below threshold (strict <)
		{1, 8, 25, true},     // 12.5% < 25%
		{5, 10, 0, false},    // pct 0 disables
		{0, 0, 25, false},    // cap 0 — nothing to reorder against
		{0, 10, 0, false},    // both off
		{100, 10, 25, false}, // over cap
		// Sub-one-unit fraction (cap*pct < 100) rounds up: reorder at the last
		// unit, not only when empty. cap 2 @ 25% = 0.5 (the skillet case, LLM-82).
		{0, 2, 25, true},  // empty
		{1, 2, 25, true},  // down to the last unit — now reorders (was the bug: fired only at 0)
		{2, 2, 25, false}, // at full cap — does not reorder
		{1, 3, 25, true},  // cap 3 @ 25% = 0.75, also sub-one-unit → fires at the last unit
	}
	for _, c := range cases {
		if got := RestockReorderThresholdMet(c.current, c.cap, c.pct); got != c.want {
			t.Errorf("RestockReorderThresholdMet(cur=%d,cap=%d,pct=%d) = %v, want %v",
				c.current, c.cap, c.pct, got, c.want)
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
// policy order is the one chosen, entries above threshold are passed over, and the
// buy source is reported. Buy entries are always actionable (no bush gate).
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
	if e, src, ok := firstActionableLowEntry(a, restockWorld(a), 25); !ok || e.Item != "salt" || src != RestockSourceBuy {
		t.Errorf("first actionable low = (%q, %q, %v), want (salt, buy, true)", e.Item, src, ok)
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
	if e, src, ok := firstActionableLowEntry(both, wBoth, 25); !ok || e.Item != "milk" || src != RestockSourceBuy {
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
	if e, src, ok := firstActionableLowEntry(forageOnly, wForage, 25); !ok || e.Item != "raspberries" || src != RestockSourceForage {
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
	if _, _, ok := firstActionableLowEntry(noBush, restockWorld(noBush), 25); ok {
		t.Error("a low forage entry with no remembered bush must not be actionable")
	}
}
