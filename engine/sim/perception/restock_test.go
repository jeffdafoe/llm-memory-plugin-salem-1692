package perception

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// restock_test.go — ZBBS-WORK-322, the "## Restocking" perception section.
// Covers the firing gate (a `buy` entry below the reorder threshold), the
// disabled/no-policy cases, supplier resolution + exclusions (structural
// vendorship reused from consumable_vendors), the price hint, and the render
// shape. Mirrors satiation_test conventions.

// restockCatalog: labels for the goods a reseller buys in.
func restockCatalog() map[sim.ItemKind]*sim.ItemKindDef {
	return map[sim.ItemKind]*sim.ItemKindDef{
		"ale":  {Name: "ale", DisplayLabel: "ale", Category: sim.ItemCategoryDrink},
		"salt": {Name: "salt", DisplayLabel: "salt", Category: sim.ItemCategoryFood},
	}
}

// buyPolicy is a one-entry buy policy for `item` at `cap`.
func buyPolicy(item sim.ItemKind, cap int) *sim.RestockPolicy {
	return &sim.RestockPolicy{Restock: []sim.RestockEntry{
		{Item: item, Source: sim.RestockSourceBuy, Max: cap},
	}}
}

func TestBuildRestocking_NoPolicy_Nil(t *testing.T) {
	subj := &sim.ActorSnapshot{Inventory: map[sim.ItemKind]int{"ale": 0}}
	snap := &sim.Snapshot{
		Actors:            map[sim.ActorID]*sim.ActorSnapshot{"merchant": subj},
		ItemKinds:         restockCatalog(),
		RestockReorderPct: 25,
	}
	if v := buildRestocking(snap, "merchant", subj); v != nil {
		t.Errorf("want nil with no RestockPolicy, got %+v", v)
	}
}

func TestBuildRestocking_DisabledPct_Nil(t *testing.T) {
	subj := &sim.ActorSnapshot{Inventory: map[sim.ItemKind]int{"ale": 0}, RestockPolicy: buyPolicy("ale", 20)}
	snap := &sim.Snapshot{
		Actors:            map[sim.ActorID]*sim.ActorSnapshot{"merchant": subj},
		ItemKinds:         restockCatalog(),
		RestockReorderPct: 0, // disabled
	}
	if v := buildRestocking(snap, "merchant", subj); v != nil {
		t.Errorf("want nil with pct=0, got %+v", v)
	}
}

func TestBuildRestocking_AboveThreshold_Nil(t *testing.T) {
	subj := &sim.ActorSnapshot{Inventory: map[sim.ItemKind]int{"ale": 10}, RestockPolicy: buyPolicy("ale", 20)} // 50%
	snap := &sim.Snapshot{
		Actors:            map[sim.ActorID]*sim.ActorSnapshot{"merchant": subj},
		ItemKinds:         restockCatalog(),
		RestockReorderPct: 25,
	}
	if v := buildRestocking(snap, "merchant", subj); v != nil {
		t.Errorf("want nil when stock above threshold, got %+v", v)
	}
}

func TestBuildRestocking_LowStockNoVendor(t *testing.T) {
	subj := &sim.ActorSnapshot{Inventory: map[sim.ItemKind]int{"ale": 3}, RestockPolicy: buyPolicy("ale", 20)} // 15%
	snap := &sim.Snapshot{
		Actors:            map[sim.ActorID]*sim.ActorSnapshot{"merchant": subj},
		ItemKinds:         restockCatalog(),
		RestockReorderPct: 25,
	}
	v := buildRestocking(snap, "merchant", subj)
	if v == nil || len(v.Items) != 1 {
		t.Fatalf("want 1 low item, got %+v", v)
	}
	it := v.Items[0]
	if it.ItemLabel != "ale" || it.CurrentQty != 3 || it.Cap != 20 {
		t.Errorf("item view = %+v, want ale 3/20", it)
	}
	if len(it.Vendors) != 0 {
		t.Errorf("want no vendors (none holding stock), got %+v", it.Vendors)
	}
}

func TestBuildRestocking_VendorResolved(t *testing.T) {
	subj := &sim.ActorSnapshot{Inventory: map[sim.ItemKind]int{"ale": 2}, RestockPolicy: buyPolicy("ale", 20)}
	supplier := &sim.ActorSnapshot{WorkStructureID: "brewery", Inventory: map[sim.ItemKind]int{"ale": 40}}
	snap := &sim.Snapshot{
		Actors: map[sim.ActorID]*sim.ActorSnapshot{"merchant": subj, "brewer": supplier},
		Structures: map[sim.StructureID]*sim.Structure{
			"brewery": {ID: "brewery", DisplayName: "The Brewery"},
		},
		ItemKinds:         restockCatalog(),
		RestockReorderPct: 25,
	}
	v := buildRestocking(snap, "merchant", subj)
	if v == nil || len(v.Items) != 1 || len(v.Items[0].Vendors) != 1 {
		t.Fatalf("want 1 vendor cue, got %+v", v)
	}
	vd := v.Items[0].Vendors[0]
	// No PriceBook entry → CostText is the empty fallback (ZBBS-HOME-386 dropped
	// the old "ask the supplier", which invited a spoken price question instead
	// of a pay_with_item call); render omits the cost clause entirely.
	if vd.StructureLabel != "The Brewery" || vd.StructureID != "brewery" || vd.CostText != "" {
		t.Errorf("vendor cue wrong: %+v", vd)
	}
}

// TestBuildRestocking_CoPresentSeller: a seller of the low item who shares the
// reseller's current huddle is surfaced as CoPresentSeller (so render can emit
// the buy-here imperative). ZBBS-HOME-388.
func TestBuildRestocking_CoPresentSeller(t *testing.T) {
	subj := &sim.ActorSnapshot{
		Inventory:       map[sim.ItemKind]int{"ale": 1},
		RestockPolicy:   buyPolicy("ale", 20),
		CurrentHuddleID: "h1",
	}
	supplier := &sim.ActorSnapshot{
		DisplayName:     "Anders Brewer",
		WorkStructureID: "brewery",
		Inventory:       map[sim.ItemKind]int{"ale": 40},
		CurrentHuddleID: "h1", // same huddle as the buyer → pay_with_item resolves now
	}
	snap := &sim.Snapshot{
		Actors:            map[sim.ActorID]*sim.ActorSnapshot{"merchant": subj, "brewer": supplier},
		Structures:        map[sim.StructureID]*sim.Structure{"brewery": {ID: "brewery", DisplayName: "The Brewery"}},
		ItemKinds:         restockCatalog(),
		RestockReorderPct: 25,
	}
	v := buildRestocking(snap, "merchant", subj)
	if v == nil || len(v.Items) != 1 {
		t.Fatalf("want 1 item, got %+v", v)
	}
	if v.Items[0].CoPresentSeller != "Anders Brewer" {
		t.Errorf("CoPresentSeller = %q, want 'Anders Brewer'", v.Items[0].CoPresentSeller)
	}
}

// TestBuildRestocking_SellerNotCoPresent: a seller in a DIFFERENT huddle (or the
// buyer in none) is not co-present — CoPresentSeller is empty, and the generic
// walk-to vendor cue still resolves. ZBBS-HOME-388.
func TestBuildRestocking_SellerNotCoPresent(t *testing.T) {
	subj := &sim.ActorSnapshot{
		Inventory:       map[sim.ItemKind]int{"ale": 1},
		RestockPolicy:   buyPolicy("ale", 20),
		CurrentHuddleID: "h1",
	}
	supplier := &sim.ActorSnapshot{
		DisplayName:     "Anders Brewer",
		WorkStructureID: "brewery",
		Inventory:       map[sim.ItemKind]int{"ale": 40},
		CurrentHuddleID: "h2", // different huddle → not co-present
	}
	snap := &sim.Snapshot{
		Actors:            map[sim.ActorID]*sim.ActorSnapshot{"merchant": subj, "brewer": supplier},
		Structures:        map[sim.StructureID]*sim.Structure{"brewery": {ID: "brewery", DisplayName: "The Brewery"}},
		ItemKinds:         restockCatalog(),
		RestockReorderPct: 25,
	}
	v := buildRestocking(snap, "merchant", subj)
	if v == nil || len(v.Items) != 1 {
		t.Fatalf("want 1 item, got %+v", v)
	}
	if v.Items[0].CoPresentSeller != "" {
		t.Errorf("CoPresentSeller = %q, want empty (different huddle)", v.Items[0].CoPresentSeller)
	}
	if len(v.Items[0].Vendors) != 1 {
		t.Errorf("walk-to vendor cue should still resolve, got %+v", v.Items[0].Vendors)
	}
}

// TestRenderRestocking_BuyHereImperative: a co-present seller renders a concrete
// pay_with_item imperative (naming the seller + canonical item + consume_now),
// suppresses the generic walk-to line for that item, and carries no "ask"/"price".
// ZBBS-HOME-388.
func TestRenderRestocking_BuyHereImperative(t *testing.T) {
	v := &RestockingView{Items: []RestockItemView{
		{
			ItemLabel: "milk", CurrentQty: 0, Cap: 20, kind: "milk",
			CoPresentSeller: "Elizabeth Ellis",
			Vendors:         []RestockVendor{{StructureLabel: "Ellis Farm", StructureID: "ellis"}},
		},
	}}
	var b strings.Builder
	renderRestocking(&b, v)
	out := b.String()
	if !strings.Contains(out, "Elizabeth Ellis is here with you") {
		t.Errorf("missing co-present seller imperative:\n%s", out)
	}
	if !strings.Contains(out, `pay_with_item with seller "Elizabeth Ellis", item "milk"`) {
		t.Errorf("missing concrete pay_with_item example:\n%s", out)
	}
	if !strings.Contains(out, "consume_now false") {
		t.Errorf("example must spell out consume_now:\n%s", out)
	}
	// ZBBS-HOME-388: order pay before speech and name the speak TOOL explicitly
	// (bubbles spawn only from speak; "say a word" alone may be satisfied as plain
	// text), so the trade both happens and is visible as a bubble.
	if !strings.Contains(out, "first call pay_with_item") || !strings.Contains(out, "use speak") {
		t.Errorf("buy-here imperative should order pay before speech:\n%s", out)
	}
	if strings.Contains(out, "buy from Ellis Farm") {
		t.Errorf("co-present item should suppress the walk-to line:\n%s", out)
	}
	lower := strings.ToLower(out)
	if strings.Contains(lower, "ask") || strings.Contains(lower, "price") {
		t.Errorf("imperative must not contain ask/price:\n%s", out)
	}
}

// TestBuildRestocking_PendingOfferToCoPresentSeller: when the reseller already has
// a still-pending pay_with_item offer to the co-present seller for the low item,
// PendingOfferToCoPresentSeller is set so render can defer to the standing offer
// instead of re-prompting the buy. LLM-64.
func TestBuildRestocking_PendingOfferToCoPresentSeller(t *testing.T) {
	subj := &sim.ActorSnapshot{
		Inventory:       map[sim.ItemKind]int{"ale": 1},
		RestockPolicy:   buyPolicy("ale", 20),
		CurrentHuddleID: "h1",
	}
	supplier := &sim.ActorSnapshot{
		DisplayName:     "Anders Brewer",
		WorkStructureID: "brewery",
		Inventory:       map[sim.ItemKind]int{"ale": 40},
		CurrentHuddleID: "h1",
	}
	snap := &sim.Snapshot{
		Actors:            map[sim.ActorID]*sim.ActorSnapshot{"merchant": subj, "brewer": supplier},
		Structures:        map[sim.StructureID]*sim.Structure{"brewery": {ID: "brewery", DisplayName: "The Brewery"}},
		ItemKinds:         restockCatalog(),
		RestockReorderPct: 25,
		PayLedger: map[sim.LedgerID]*sim.PayLedgerEntry{
			1: offerEntry(1, "merchant", "brewer", "ale", 10, 30, sim.PayLedgerStatePending),
		},
	}
	v := buildRestocking(snap, "merchant", subj)
	if v == nil || len(v.Items) != 1 {
		t.Fatalf("want 1 item, got %+v", v)
	}
	if !v.Items[0].PendingOfferToCoPresentSeller {
		t.Errorf("PendingOfferToCoPresentSeller = false, want true (standing offer to the co-present seller)")
	}
}

// TestBuildRestocking_PendingOfferToOtherSeller_FlagUnset: a pending offer for the
// item to a DIFFERENT seller than the co-present one must NOT set the flag — the
// reseller can still buy from the seller in front of them. Guards the seller-id
// narrowing of the hasPendingOfferTo check. LLM-64.
func TestBuildRestocking_PendingOfferToOtherSeller_FlagUnset(t *testing.T) {
	subj := &sim.ActorSnapshot{
		Inventory:       map[sim.ItemKind]int{"ale": 1},
		RestockPolicy:   buyPolicy("ale", 20),
		CurrentHuddleID: "h1",
	}
	supplier := &sim.ActorSnapshot{
		DisplayName:     "Anders Brewer",
		WorkStructureID: "brewery",
		Inventory:       map[sim.ItemKind]int{"ale": 40},
		CurrentHuddleID: "h1",
	}
	snap := &sim.Snapshot{
		Actors:            map[sim.ActorID]*sim.ActorSnapshot{"merchant": subj, "brewer": supplier},
		Structures:        map[sim.StructureID]*sim.Structure{"brewery": {ID: "brewery", DisplayName: "The Brewery"}},
		ItemKinds:         restockCatalog(),
		RestockReorderPct: 25,
		PayLedger: map[sim.LedgerID]*sim.PayLedgerEntry{
			1: offerEntry(1, "merchant", "someone_else", "ale", 10, 30, sim.PayLedgerStatePending),
		},
	}
	v := buildRestocking(snap, "merchant", subj)
	if v == nil || len(v.Items) != 1 {
		t.Fatalf("want 1 item, got %+v", v)
	}
	if v.Items[0].CoPresentSeller != "Anders Brewer" {
		t.Fatalf("CoPresentSeller = %q, want 'Anders Brewer'", v.Items[0].CoPresentSeller)
	}
	if v.Items[0].PendingOfferToCoPresentSeller {
		t.Errorf("PendingOfferToCoPresentSeller = true, want false (offer is to a different seller)")
	}
}

// TestRenderRestocking_PendingOfferWaitLine: with the pending-offer flag set, the
// item renders a stay-and-wait steer and DROPS the headroom/cost lines, the "buy
// it now" imperative, and the walk-to list — so the reseller bides instead of
// re-staking the offer or walking off. LLM-64.
func TestRenderRestocking_PendingOfferWaitLine(t *testing.T) {
	v := &RestockingView{
		Items: []RestockItemView{
			{
				ItemLabel: "milk", CurrentQty: 4, Cap: 20, kind: "milk",
				CoPresentSeller:               "Elizabeth Ellis",
				PendingOfferToCoPresentSeller: true,
				Vendors:                       []RestockVendor{{StructureLabel: "Ellis Farm", StructureID: "ellis"}},
			},
		},
		BuyerCoins: 172,
	}
	var b strings.Builder
	renderRestocking(&b, v)
	out := b.String()
	if !strings.Contains(out, "Elizabeth Ellis is here with you, and your offer for milk is still with them") {
		t.Errorf("missing stay-and-wait line:\n%s", out)
	}
	if !strings.Contains(out, "Wait here for their answer") {
		t.Errorf("missing wait steer:\n%s", out)
	}
	if strings.Contains(out, "Buy it now") || strings.Contains(out, "pay_with_item") {
		t.Errorf("pending offer must suppress the buy imperative:\n%s", out)
	}
	if strings.Contains(out, "room for") || strings.Contains(out, "Filling to cap") {
		t.Errorf("pending offer must suppress the headroom/cost lines:\n%s", out)
	}
	if strings.Contains(out, "buy from Ellis Farm") {
		t.Errorf("pending offer must suppress the walk-to list:\n%s", out)
	}
}

// TestBuildRestocking_CoPresentSeller_Deterministic: with two co-present sellers
// of the item, the imperative names the lowest-VendorID one deterministically so
// the cue is stable regardless of snapshot map-iteration order. Looped to catch
// nondeterminism, same posture as TestFindItemVendors_DedupeByStructure. ZBBS-HOME-388.
func TestBuildRestocking_CoPresentSeller_Deterministic(t *testing.T) {
	subj := &sim.ActorSnapshot{
		Inventory:       map[sim.ItemKind]int{"ale": 1},
		RestockPolicy:   buyPolicy("ale", 20),
		CurrentHuddleID: "h1",
	}
	ann := &sim.ActorSnapshot{DisplayName: "Ann", WorkStructureID: "brewery", Inventory: map[sim.ItemKind]int{"ale": 40}, CurrentHuddleID: "h1"}
	zed := &sim.ActorSnapshot{DisplayName: "Zed", WorkStructureID: "brewery", Inventory: map[sim.ItemKind]int{"ale": 40}, CurrentHuddleID: "h1"}
	snap := &sim.Snapshot{
		Actors:            map[sim.ActorID]*sim.ActorSnapshot{"merchant": subj, "z_seller": zed, "a_seller": ann},
		Structures:        map[sim.StructureID]*sim.Structure{"brewery": {ID: "brewery", DisplayName: "The Brewery"}},
		ItemKinds:         restockCatalog(),
		RestockReorderPct: 25,
	}
	for i := 0; i < 30; i++ {
		v := buildRestocking(snap, "merchant", subj)
		if v == nil || len(v.Items) != 1 {
			t.Fatalf("want 1 item, got %+v", v)
		}
		// lowest VendorID ("a_seller" < "z_seller") → Ann, regardless of map order.
		if v.Items[0].CoPresentSeller != "Ann" {
			t.Fatalf("CoPresentSeller = %q, want 'Ann' (lowest-VendorID rep)", v.Items[0].CoPresentSeller)
		}
	}
}

// TestBuildRestocking_VendorExclusions: self, PC suppliers, no-workplace, and
// unresolvable-structure suppliers are all excluded.
func TestBuildRestocking_VendorExclusions(t *testing.T) {
	subj := &sim.ActorSnapshot{Inventory: map[sim.ItemKind]int{"ale": 1}, RestockPolicy: buyPolicy("ale", 20)}
	// PC holding ale — excluded (PCs don't sell through the NPC commerce path).
	pcSeller := &sim.ActorSnapshot{Kind: sim.KindPC, WorkStructureID: "brewery", Inventory: map[sim.ItemKind]int{"ale": 40}}
	// No workplace — excluded.
	noWork := &sim.ActorSnapshot{Inventory: map[sim.ItemKind]int{"ale": 40}}
	// Workplace not in snapshot.Structures — excluded (unactionable destination).
	ghost := &sim.ActorSnapshot{WorkStructureID: "nowhere", Inventory: map[sim.ItemKind]int{"ale": 40}}
	snap := &sim.Snapshot{
		Actors: map[sim.ActorID]*sim.ActorSnapshot{
			"merchant": subj, "pc": pcSeller, "drifter": noWork, "ghost": ghost,
		},
		Structures:        map[sim.StructureID]*sim.Structure{"brewery": {ID: "brewery", DisplayName: "The Brewery"}},
		ItemKinds:         restockCatalog(),
		RestockReorderPct: 25,
	}
	v := buildRestocking(snap, "merchant", subj)
	if v == nil || len(v.Items) != 1 {
		t.Fatalf("want the low item, got %+v", v)
	}
	if len(v.Items[0].Vendors) != 0 {
		t.Errorf("all suppliers should be excluded, got %+v", v.Items[0].Vendors)
	}
}

// TestBuildRestocking_ProduceEntriesIgnored: a low PRODUCE entry doesn't surface
// here (that's the produce side, not restock).
func TestBuildRestocking_ProduceEntriesIgnored(t *testing.T) {
	subj := &sim.ActorSnapshot{
		Inventory: map[sim.ItemKind]int{"bread": 0},
		RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: "bread", Source: sim.RestockSourceProduce, Max: 20},
		}},
	}
	snap := &sim.Snapshot{
		Actors:            map[sim.ActorID]*sim.ActorSnapshot{"baker": subj},
		ItemKinds:         restockCatalog(),
		RestockReorderPct: 25,
	}
	if v := buildRestocking(snap, "baker", subj); v != nil {
		t.Errorf("produce entry must not surface in restocking, got %+v", v)
	}
}

// TestFindItemVendors_DedupeByStructure: two suppliers at the same structure
// both holding the item collapse to ONE cue, deterministically attributed to the
// lowest VendorID (so the per-buyer price hint is stable). Runs many times to
// catch map-order nondeterminism.
func TestFindItemVendors_DedupeByStructure(t *testing.T) {
	subj := &sim.ActorSnapshot{Inventory: map[sim.ItemKind]int{"ale": 1}, RestockPolicy: buyPolicy("ale", 20)}
	// Two brewers at the same structure; "anders" (< "bramble") is the rep.
	anders := &sim.ActorSnapshot{WorkStructureID: "brewery", Inventory: map[sim.ItemKind]int{"ale": 40}}
	bramble := &sim.ActorSnapshot{WorkStructureID: "brewery", Inventory: map[sim.ItemKind]int{"ale": 40}}
	pbAnders := sim.NewRingBuffer[sim.PriceObservation](4)
	pbAnders.Push(sim.PriceObservation{BuyerID: "merchant", Amount: 2, Qty: 1, Consumers: 1, At: time.Now().UTC()})
	pbBramble := sim.NewRingBuffer[sim.PriceObservation](4)
	pbBramble.Push(sim.PriceObservation{BuyerID: "merchant", Amount: 9, Qty: 1, Consumers: 1, At: time.Now().UTC()})
	snap := &sim.Snapshot{
		Actors:     map[sim.ActorID]*sim.ActorSnapshot{"merchant": subj, "anders": anders, "bramble": bramble},
		Structures: map[sim.StructureID]*sim.Structure{"brewery": {ID: "brewery", DisplayName: "The Brewery"}},
		ItemKinds:  restockCatalog(),
		PriceBook: map[sim.PriceBookKey]*sim.RingBuffer[sim.PriceObservation]{
			{SellerID: "anders", Item: "ale"}:  pbAnders,
			{SellerID: "bramble", Item: "ale"}: pbBramble,
		},
		RestockReorderPct: 25,
	}
	for i := 0; i < 30; i++ {
		v := buildRestocking(snap, "merchant", subj)
		if v == nil || len(v.Items) != 1 || len(v.Items[0].Vendors) != 1 {
			t.Fatalf("want exactly 1 deduped vendor cue, got %+v", v)
		}
		// Lowest VendorID is "anders" → its price (~2), never bramble's (~9).
		if got := v.Items[0].Vendors[0].CostText; got != "~2 coins" {
			t.Fatalf("CostText = %q, want '~2 coins' (lowest-VendorID rep, deterministic)", got)
		}
	}
}

func TestBuildRestocking_PriceFromPriceBook(t *testing.T) {
	subj := &sim.ActorSnapshot{Inventory: map[sim.ItemKind]int{"ale": 1}, RestockPolicy: buyPolicy("ale", 20)}
	supplier := &sim.ActorSnapshot{WorkStructureID: "brewery", Inventory: map[sim.ItemKind]int{"ale": 40}}
	pb := sim.NewRingBuffer[sim.PriceObservation](4)
	pb.Push(sim.PriceObservation{BuyerID: "merchant", Amount: 2, Qty: 1, Consumers: 1, At: time.Now().UTC()})
	snap := &sim.Snapshot{
		Actors:     map[sim.ActorID]*sim.ActorSnapshot{"merchant": subj, "brewer": supplier},
		Structures: map[sim.StructureID]*sim.Structure{"brewery": {ID: "brewery", DisplayName: "The Brewery"}},
		ItemKinds:  restockCatalog(),
		PriceBook: map[sim.PriceBookKey]*sim.RingBuffer[sim.PriceObservation]{
			{SellerID: "brewer", Item: "ale"}: pb,
		},
		RestockReorderPct: 25,
	}
	v := buildRestocking(snap, "merchant", subj)
	if v == nil || len(v.Items) != 1 || len(v.Items[0].Vendors) != 1 {
		t.Fatalf("want 1 vendor cue, got %+v", v)
	}
	if got := v.Items[0].Vendors[0].CostText; got != "~2 coins" {
		t.Errorf("CostText = %q, want '~2 coins' (last-paid)", got)
	}
}

// TestBuildRestocking_AffordabilityFromBundleRatio (ZBBS-HOME-459 / code_review):
// the affordable count comes straight from the observed bundle ratio, not a
// floored unit price. Last paid 5 coins for 2 ale; 9 coins covers 3 at that rate
// — a floored unit price (5/2 = 2) would wrongly read 9/2 = 4 and over-promise.
func TestBuildRestocking_AffordabilityFromBundleRatio(t *testing.T) {
	subj := &sim.ActorSnapshot{Coins: 9, Inventory: map[sim.ItemKind]int{"ale": 1}, RestockPolicy: buyPolicy("ale", 20)}
	supplier := &sim.ActorSnapshot{WorkStructureID: "brewery", Inventory: map[sim.ItemKind]int{"ale": 40}}
	pb := sim.NewRingBuffer[sim.PriceObservation](4)
	pb.Push(sim.PriceObservation{BuyerID: "merchant", Amount: 5, Qty: 2, Consumers: 1, At: time.Now().UTC()})
	snap := &sim.Snapshot{
		Actors:     map[sim.ActorID]*sim.ActorSnapshot{"merchant": subj, "brewer": supplier},
		Structures: map[sim.StructureID]*sim.Structure{"brewery": {ID: "brewery", DisplayName: "The Brewery"}},
		ItemKinds:  restockCatalog(),
		PriceBook: map[sim.PriceBookKey]*sim.RingBuffer[sim.PriceObservation]{
			{SellerID: "brewer", Item: "ale"}: pb,
		},
		RestockReorderPct: 25,
	}
	v := buildRestocking(snap, "merchant", subj)
	if v == nil || len(v.Items) != 1 {
		t.Fatalf("want 1 item, got %+v", v)
	}
	if got := v.Items[0].AffordableQty; got != 3 {
		t.Errorf("AffordableQty = %d, want 3 (9 coins at the 5-for-2 rate, not the floored-unit-price 4)", got)
	}
	var b strings.Builder
	renderRestocking(&b, v)
	if out := b.String(); !strings.Contains(out, "cover about 3") || strings.Contains(out, "cover about 4") {
		t.Errorf("want 'cover about 3' (not 4):\n%s", out)
	}
}

// TestBuildRestocking_AffordabilityDeterministicOnTimestampTie (code_review):
// two sellers, identical observation timestamp, different rates — the lowest
// seller id must win deterministically rather than tracking map-iteration order.
func TestBuildRestocking_AffordabilityDeterministicOnTimestampTie(t *testing.T) {
	subj := &sim.ActorSnapshot{Coins: 100, Inventory: map[sim.ItemKind]int{"ale": 1}, RestockPolicy: buyPolicy("ale", 20)}
	at := time.Now().UTC()
	pbAnders := sim.NewRingBuffer[sim.PriceObservation](4)
	pbAnders.Push(sim.PriceObservation{BuyerID: "merchant", Amount: 10, Qty: 1, Consumers: 1, At: at}) // 10/unit → 10 affordable
	pbBramble := sim.NewRingBuffer[sim.PriceObservation](4)
	pbBramble.Push(sim.PriceObservation{BuyerID: "merchant", Amount: 5, Qty: 1, Consumers: 1, At: at}) // 5/unit → 20 affordable
	snap := &sim.Snapshot{
		Actors:    map[sim.ActorID]*sim.ActorSnapshot{"merchant": subj},
		ItemKinds: restockCatalog(),
		PriceBook: map[sim.PriceBookKey]*sim.RingBuffer[sim.PriceObservation]{
			{SellerID: "anders", Item: "ale"}:  pbAnders,
			{SellerID: "bramble", Item: "ale"}: pbBramble,
		},
		RestockReorderPct: 25,
	}
	for i := 0; i < 30; i++ {
		v := buildRestocking(snap, "merchant", subj)
		if v == nil || len(v.Items) != 1 {
			t.Fatalf("want 1 item, got %+v", v)
		}
		// anders (lowest id) at 10/unit → 100 coins cover 10, never bramble's 20.
		if got := v.Items[0].AffordableQty; got != 10 {
			t.Fatalf("AffordableQty = %d, want 10 (anders wins the timestamp tie deterministically)", got)
		}
	}
}

// TestRenderRestocking_AffordabilityFact (ZBBS-HOME-459): the per-item purse
// fact appears only when coins are the binding limit (fewer affordable than the
// cap leaves room for) and a unit price is on record. The wording carries no
// "ask"/"price"/"cost" token (HOME-386-safe).
func TestRenderRestocking_AffordabilityFact(t *testing.T) {
	// Coins bind before the cap: headroom 18, purse covers 6 → fact renders.
	var bind strings.Builder
	renderRestocking(&bind, &RestockingView{
		BuyerCoins: 60,
		Items:      []RestockItemView{{ItemLabel: "meat", CurrentQty: 2, Cap: 20, AffordableQty: 6}},
	})
	if out := bind.String(); !strings.Contains(out, "Your 60 coins cover about 6 at what you last paid.") {
		t.Errorf("binding-purse fact missing:\n%s", out)
	}
	if out := bind.String(); strings.Contains(out, "price") || strings.Contains(out, "ask") || strings.Contains(out, "cost") {
		t.Errorf("affordability fact leaked an ask/price/cost token (HOME-386):\n%s", out)
	}

	// Purse covers more than the cap leaves room for → coins aren't the limit.
	var slack strings.Builder
	renderRestocking(&slack, &RestockingView{
		BuyerCoins: 1000,
		Items:      []RestockItemView{{ItemLabel: "meat", CurrentQty: 2, Cap: 20, AffordableQty: 50}},
	})
	if out := slack.String(); strings.Contains(out, "cover about") {
		t.Errorf("purse covering the headroom should add no fact:\n%s", out)
	}

	// No price on record (-1) → silent.
	var unknown strings.Builder
	renderRestocking(&unknown, &RestockingView{
		BuyerCoins: 60,
		Items:      []RestockItemView{{ItemLabel: "meat", CurrentQty: 2, Cap: 20, AffordableQty: -1}},
	})
	if out := unknown.String(); strings.Contains(out, "cover about") {
		t.Errorf("unknown price should render no affordability fact:\n%s", out)
	}
}

func TestRenderRestocking_NilAndEmpty(t *testing.T) {
	var b strings.Builder
	renderRestocking(&b, nil)
	renderRestocking(&b, &RestockingView{})
	if b.String() != "" {
		t.Errorf("nil/empty restocking should render nothing, got %q", b.String())
	}
}

func TestRenderRestocking_Shape(t *testing.T) {
	v := &RestockingView{Items: []RestockItemView{
		{
			ItemLabel: "ale", CurrentQty: 2, Cap: 20,
			Vendors: []RestockVendor{{StructureLabel: "The Brewery", StructureID: "brewery", CostText: "~2 coins"}},
		},
		{ItemLabel: "salt", CurrentQty: 0, Cap: 10}, // no vendor
	}}
	var b strings.Builder
	renderRestocking(&b, v)
	out := b.String()
	if !strings.Contains(out, "## Restocking") {
		t.Error("missing section header")
	}
	if !strings.Contains(out, "You have 2 ale on hand and room for 18 more at the most.") {
		t.Errorf("missing ale on-hand + capacity line:\n%s", out)
	}
	if strings.Contains(out, "Filling to cap") || strings.Contains(out, "of 20 cap") {
		t.Errorf("LLM-63: the fill-to-cap price anchor must be gone:\n%s", out)
	}
	if !strings.Contains(out, "buy from The Brewery (structure_id: brewery), ~2 coins") {
		t.Errorf("missing vendor line:\n%s", out)
	}
	if !strings.Contains(out, "You have 0 salt on hand and room for 10 more at the most.") {
		t.Errorf("missing salt line:\n%s", out)
	}
	if !strings.Contains(out, "No supplier nearby is currently holding stock.") {
		t.Errorf("missing no-supplier line:\n%s", out)
	}
	// ZBBS-HOME-386: the section names the action (move_to + pay_with_item) as a
	// two-step sequence and carries neither "ask" nor "price" — negated ask/price
	// wording still primes the speak-loop on a weak model (code_review). (Test
	// inputs control the labels, so a whole-render substring check is safe here.)
	if !strings.Contains(out, "move_to") || !strings.Contains(out, "pay_with_item") {
		t.Errorf("section should name the move_to + pay_with_item action:\n%s", out)
	}
	lower := strings.ToLower(out)
	if strings.Contains(lower, "ask") || strings.Contains(lower, "price") {
		t.Errorf("cue should not contain 'ask'/'price' (primes the speak-loop):\n%s", out)
	}
}

// TestRenderRestocking_NoPriceOmitsCost: a vendor with no price on record
// (CostText == "") renders just the destination, with no trailing ", ..." cost
// clause — the ZBBS-HOME-386 replacement for the old "ask the supplier" hint.
func TestRenderRestocking_NoPriceOmitsCost(t *testing.T) {
	v := &RestockingView{Items: []RestockItemView{
		{
			ItemLabel: "milk", CurrentQty: 0, Cap: 20,
			Vendors: []RestockVendor{{StructureLabel: "Ellis Farm", StructureID: "ellis", CostText: ""}},
		},
	}}
	var b strings.Builder
	renderRestocking(&b, v)
	out := b.String()
	if !strings.Contains(out, "buy from Ellis Farm (structure_id: ellis)") {
		t.Errorf("missing vendor destination line:\n%s", out)
	}
	// Empty CostText must not render a trailing ", <cost>" clause after the id.
	if strings.Contains(out, "(structure_id: ellis),") {
		t.Errorf("empty CostText should not render a trailing cost clause:\n%s", out)
	}
}

// --- experiential remembered-shut (LLM-126) ---------------------------

// TestBuildRestocking_RememberedShutSupplierNotDemoted — LLM-126, decision 1(a). A
// supplier the buyer remembers finding shut (a decaying ObservedClosed memory) is
// flagged Shut but keeps its natural (alphabetical) position rather than being sunk
// below an open one: the omniscient live-asleep read AND its open-before-closed sort
// sink were retired together, so a remembered-shut supplier is annotated, not
// demoted. A supplier the buyer has never visited carries no flag at all.
func TestBuildRestocking_RememberedShutSupplierNotDemoted(t *testing.T) {
	now := time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC)
	subj := &sim.ActorSnapshot{
		Inventory:     map[sim.ItemKind]int{"ale": 2},
		RestockPolicy: buyPolicy("ale", 20),
		// He remembers the Abbey (alphabetically first) shut; the Brewery he does not.
		Observed: sim.NewObservedStates(map[sim.ObservedStateKey]time.Time{
			{StructureID: "abbey", Condition: sim.ObservedClosed}: now.Add(-time.Hour),
		}),
	}
	amos := &sim.ActorSnapshot{WorkStructureID: "abbey", Inventory: map[sim.ItemKind]int{"ale": 40}, State: sim.StateIdle}
	bram := &sim.ActorSnapshot{WorkStructureID: "brewery", Inventory: map[sim.ItemKind]int{"ale": 40}, State: sim.StateIdle}
	snap := &sim.Snapshot{
		PublishedAt: now,
		Actors:      map[sim.ActorID]*sim.ActorSnapshot{"merchant": subj, "amos": amos, "bram": bram},
		Structures: map[sim.StructureID]*sim.Structure{
			"abbey":   {ID: "abbey", DisplayName: "Abbey Brewhouse"},
			"brewery": {ID: "brewery", DisplayName: "The Brewery"},
		},
		ItemKinds:         restockCatalog(),
		RestockReorderPct: 25,
	}
	v := buildRestocking(snap, "merchant", subj)
	if v == nil || len(v.Items) != 1 || len(v.Items[0].Vendors) != 2 {
		t.Fatalf("want 2 supplier cues, got %+v", v)
	}
	vds := v.Items[0].Vendors
	// Alphabetical by label (Abbey Brewhouse < The Brewery); the remembered-shut
	// Abbey is NOT sunk below the open Brewery.
	if vds[0].StructureID != "abbey" || !vds[0].Shut {
		t.Errorf("remembered-shut Abbey must keep its alphabetical lead (not demoted), got first = %+v", vds[0])
	}
	if vds[1].StructureID != "brewery" || vds[1].Shut {
		t.Errorf("the un-remembered Brewery must follow and not be flagged shut, got second = %+v", vds[1])
	}
}

// TestRenderRestocking_WalkToInstruction (LLM-10): a walk-to item (a vendor, no
// co-present seller) names the actual no-seller-here situation and the two-step
// move_to → pay_with_item buy; the co-present item does not.
func TestRenderRestocking_WalkToInstruction(t *testing.T) {
	var walk strings.Builder
	renderRestocking(&walk, &RestockingView{Items: []RestockItemView{
		{ItemLabel: "milk", CurrentQty: 4, Cap: 19,
			Vendors: []RestockVendor{{StructureLabel: "Ellis Farm", StructureID: "ellis"}}},
	}})
	if out := walk.String(); !strings.Contains(out, "No seller is here now — use move_to to reach a supplier below, then pay_with_item once you arrive.") {
		t.Errorf("walk-to item should name the no-seller situation + the two-step buy:\n%s", out)
	}

	var here strings.Builder
	renderRestocking(&here, &RestockingView{Items: []RestockItemView{
		{ItemLabel: "milk", CurrentQty: 4, Cap: 19, kind: "milk",
			CoPresentSeller: "Elizabeth Ellis",
			Vendors:         []RestockVendor{{StructureLabel: "Ellis Farm", StructureID: "ellis"}}},
	}})
	out := here.String()
	if strings.Contains(out, "No seller is here now") {
		t.Errorf("co-present item must NOT carry the no-seller walk-to instruction:\n%s", out)
	}
	if !strings.Contains(out, "Elizabeth Ellis is here with you") {
		t.Errorf("co-present item should carry the buy-here imperative:\n%s", out)
	}
}

// TestRenderRestocking_HeaderNeutral (LLM-10): the section header no longer hedges
// "if a seller is here / otherwise" nor invites filling to cap — the per-item lines
// carry the situational instruction now.
func TestRenderRestocking_HeaderNeutral(t *testing.T) {
	var b strings.Builder
	renderRestocking(&b, &RestockingView{Items: []RestockItemView{{ItemLabel: "milk", CurrentQty: 4, Cap: 19}}})
	out := b.String()
	if !strings.Contains(out, "Your shop stock of these bought-in goods is running low. You choose how much to buy.") {
		t.Errorf("missing neutral header:\n%s", out)
	}
	if strings.Contains(out, "If a seller is here") || strings.Contains(out, "up to your cap") {
		t.Errorf("header should no longer hedge seller-presence or invite filling to cap:\n%s", out)
	}
}

// --- LLM-63: recent sell-through demand signal -----------------------------

// TestBuildRestocking_RecentSalesUnits: the reseller's weekly economics for the low
// item — units sold + coins taken in (seller view), and coins paid restocking
// (buyer view) — are summed over the trailing window, with stale observations
// excluded from all three.
func TestBuildRestocking_RecentSalesUnits(t *testing.T) {
	now := time.Date(2026, 6, 23, 12, 0, 0, 0, time.UTC)
	subj := &sim.ActorSnapshot{Inventory: map[sim.ItemKind]int{"ale": 2}, RestockPolicy: buyPolicy("ale", 20)}
	// merchant-as-SELLER ring for ale: a 2×3 = 6-unit sale (12 coins) and a solo
	// 4-unit sale (8 coins) in window, plus a stale 99-unit sale 10 days back.
	pb := sim.NewRingBuffer[sim.PriceObservation](20)
	pb.Push(sim.PriceObservation{BuyerID: "old", Amount: 99, Qty: 99, Consumers: 1, At: now.Add(-10 * 24 * time.Hour)})
	pb.Push(sim.PriceObservation{BuyerID: "b1", Amount: 12, Qty: 2, Consumers: 3, At: now.Add(-2 * 24 * time.Hour)})
	pb.Push(sim.PriceObservation{BuyerID: "b2", Amount: 8, Qty: 4, Consumers: 1, At: now.Add(-1 * time.Hour)})
	// merchant-as-BUYER ring (a supplier it restocks ale from): one in-window
	// purchase (7 coins) and one stale purchase (50 coins, excluded from the cost).
	buyPB := sim.NewRingBuffer[sim.PriceObservation](20)
	buyPB.Push(sim.PriceObservation{BuyerID: "merchant", Amount: 50, Qty: 6, Consumers: 1, At: now.Add(-9 * 24 * time.Hour)})
	buyPB.Push(sim.PriceObservation{BuyerID: "merchant", Amount: 7, Qty: 1, Consumers: 1, At: now.Add(-3 * time.Hour)})
	snap := &sim.Snapshot{
		PublishedAt: now,
		Actors:      map[sim.ActorID]*sim.ActorSnapshot{"merchant": subj},
		ItemKinds:   restockCatalog(),
		PriceBook: map[sim.PriceBookKey]*sim.RingBuffer[sim.PriceObservation]{
			{SellerID: "merchant", Item: "ale"}: pb,
			{SellerID: "supplier", Item: "ale"}: buyPB,
		},
		RestockReorderPct: 25,
	}
	v := buildRestocking(snap, "merchant", subj)
	if v == nil || len(v.Items) != 1 {
		t.Fatalf("want 1 item, got %+v", v)
	}
	// 6 (2×3) + 4 (4×1) = 10 in-window units; the 10-day-old 99 is excluded.
	if got := v.Items[0].RecentSalesUnits; got != 10 {
		t.Errorf("RecentSalesUnits = %d, want 10 (6+4 in-window, stale 99 excluded)", got)
	}
	if got := v.Items[0].RecentSalesCoins; got != 20 {
		t.Errorf("RecentSalesCoins = %d, want 20 (12+8 in-window sale amounts)", got)
	}
	if got := v.Items[0].RecentBuyCost; got != 7 {
		t.Errorf("RecentBuyCost = %d, want 7 (in-window purchase only; stale 50 excluded)", got)
	}
}

// TestBuildRestocking_RecentSalesUnits_NoSellerHistory: a ring keyed to a DIFFERENT
// seller is not read as the reseller's sales — RecentSalesUnits is 0 so the cue
// stays silent rather than asserting a rate from someone else's book.
func TestBuildRestocking_RecentSalesUnits_NoSellerHistory(t *testing.T) {
	now := time.Date(2026, 6, 23, 12, 0, 0, 0, time.UTC)
	subj := &sim.ActorSnapshot{Inventory: map[sim.ItemKind]int{"ale": 2}, RestockPolicy: buyPolicy("ale", 20)}
	pb := sim.NewRingBuffer[sim.PriceObservation](20)
	pb.Push(sim.PriceObservation{BuyerID: "merchant", Amount: 4, Qty: 4, Consumers: 1, At: now.Add(-1 * time.Hour)})
	snap := &sim.Snapshot{
		PublishedAt: now,
		Actors:      map[sim.ActorID]*sim.ActorSnapshot{"merchant": subj},
		ItemKinds:   restockCatalog(),
		PriceBook: map[sim.PriceBookKey]*sim.RingBuffer[sim.PriceObservation]{
			{SellerID: "someone_else", Item: "ale"}: pb,
		},
		RestockReorderPct: 25,
	}
	v := buildRestocking(snap, "merchant", subj)
	if v == nil || len(v.Items) != 1 {
		t.Fatalf("want 1 item, got %+v", v)
	}
	if got := v.Items[0].RecentSalesUnits; got != 0 {
		t.Errorf("RecentSalesUnits = %d, want 0 (no merchant-as-seller ring)", got)
	}
}

// TestBuyerRecentPurchases_BoundaryAndFilter pins the buyer-leg PriceBook scan that
// feeds the reseller cost basis (LLM-191): purchases are summed across all sellers
// of the item, filtered to this buyer, with the window cutoff INCLUSIVE of an
// observation exactly at it and EXCLUSIVE of one just before it; units are
// Qty×Consumers.
func TestBuyerRecentPurchases_BoundaryAndFilter(t *testing.T) {
	now := time.Date(2026, 6, 23, 12, 0, 0, 0, time.UTC)
	window := restockSalesWindow
	cutoff := now.Add(-window)
	// Two suppliers of cheese, both bought by "merchant" in-window (8 coins/4 units
	// and 6 coins/2 units = 14 coins / 6 units), plus a different-buyer obs and a
	// just-before-cutoff obs that must both be excluded.
	s1 := sim.NewRingBuffer[sim.PriceObservation](8)
	s1.Push(sim.PriceObservation{BuyerID: "merchant", Amount: 8, Qty: 4, Consumers: 1, At: now.Add(-2 * time.Hour)})
	s1.Push(sim.PriceObservation{BuyerID: "other", Amount: 99, Qty: 9, Consumers: 1, At: now.Add(-1 * time.Hour)})     // different buyer — ignored
	s1.Push(sim.PriceObservation{BuyerID: "merchant", Amount: 50, Qty: 5, Consumers: 1, At: cutoff.Add(-time.Second)}) // before cutoff — excluded
	s2 := sim.NewRingBuffer[sim.PriceObservation](8)
	s2.Push(sim.PriceObservation{BuyerID: "merchant", Amount: 6, Qty: 2, Consumers: 1, At: cutoff}) // exactly at cutoff — included
	// A different item the merchant also bought — must not bleed into the cheese total.
	milk := sim.NewRingBuffer[sim.PriceObservation](8)
	milk.Push(sim.PriceObservation{BuyerID: "merchant", Amount: 100, Qty: 10, Consumers: 1, At: now.Add(-1 * time.Hour)})
	snap := &sim.Snapshot{
		PublishedAt: now,
		PriceBook: map[sim.PriceBookKey]*sim.RingBuffer[sim.PriceObservation]{
			{SellerID: "s1", Item: "cheese"}: s1,
			{SellerID: "s2", Item: "cheese"}: s2,
			{SellerID: "s1", Item: "milk"}:   milk,
		},
	}
	if units, coins := buyerRecentPurchases(snap, "merchant", "cheese", window); units != 6 || coins != 14 {
		t.Fatalf("buyerRecentPurchases = (%d units, %d coins), want (6, 14)", units, coins)
	}
	// Consumers multiply units: Qty 2 × Consumers 3 = 6 units for 3 coins.
	multi := sim.NewRingBuffer[sim.PriceObservation](4)
	multi.Push(sim.PriceObservation{BuyerID: "m", Amount: 3, Qty: 2, Consumers: 3, At: now.Add(-1 * time.Hour)})
	snap2 := &sim.Snapshot{
		PublishedAt: now,
		PriceBook: map[sim.PriceBookKey]*sim.RingBuffer[sim.PriceObservation]{
			{SellerID: "s", Item: "cheese"}: multi,
		},
	}
	if u, c := buyerRecentPurchases(snap2, "m", "cheese", window); u != 6 || c != 3 {
		t.Errorf("Qty×Consumers units: got (%d, %d), want (6, 3)", u, c)
	}
}

// TestRenderRestocking_SellThroughLine: a positive RecentSalesUnits renders the
// "you've sold about N over the past week" demand line after the on-hand lead;
// zero renders nothing.
func TestRenderRestocking_SellThroughLine(t *testing.T) {
	var sold strings.Builder
	renderRestocking(&sold, &RestockingView{Items: []RestockItemView{
		{ItemLabel: "milk", CurrentQty: 4, Cap: 20, RecentSalesUnits: 18, RecentBuyCost: 130, RecentSalesCoins: 150},
	}})
	out := sold.String()
	if !strings.Contains(out, "You have 4 milk on hand and room for 16 more at the most.") {
		t.Errorf("missing on-hand + capacity lead:\n%s", out)
	}
	if !strings.Contains(out, "You've sold about 18 over the past week, at a cost of 130 coins and sales of 150 coins.") {
		t.Errorf("missing sell-through + P&L line:\n%s", out)
	}

	// Zero units sold suppresses the whole demand/P&L sentence (no rate to assert),
	// even if a buy cost happens to be on record.
	var silent strings.Builder
	renderRestocking(&silent, &RestockingView{Items: []RestockItemView{
		{ItemLabel: "milk", CurrentQty: 4, Cap: 20, RecentSalesUnits: 0, RecentBuyCost: 5, RecentSalesCoins: 0},
	}})
	if out := silent.String(); strings.Contains(out, "over the past week") {
		t.Errorf("zero recent sales should render no sell-through/P&L line:\n%s", out)
	}
}
