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
				FillCost:                      128,
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
	if !strings.Contains(out, "ale: 2 on hand of 20 cap (room for 18 more).") {
		t.Errorf("missing ale headroom line:\n%s", out)
	}
	if !strings.Contains(out, "buy from The Brewery (structure_id: brewery), ~2 coins") {
		t.Errorf("missing vendor line:\n%s", out)
	}
	if !strings.Contains(out, "salt: 0 on hand of 10 cap (room for 10 more).") {
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

// --- live keeper-asleep gate (ZBBS-HOME-406) --------------------------

// TestBuildRestocking_ClosedNowWhenKeeperAsleep: a supplier whose only keeper of
// the low item is asleep is annotated ClosedNow (a live snapshot read), so the
// cue can read "no one tending it just now" rather than baiting the merchant to
// petition an unreachable seller in a loop (the Josiah↔Tavern spiral). The
// buyer-side mirror of satiation's keeper-asleep gate (ZBBS-HOME-387).
func TestBuildRestocking_ClosedNowWhenKeeperAsleep(t *testing.T) {
	subj := &sim.ActorSnapshot{Inventory: map[sim.ItemKind]int{"ale": 2}, RestockPolicy: buyPolicy("ale", 20)}
	supplier := &sim.ActorSnapshot{WorkStructureID: "brewery", Inventory: map[sim.ItemKind]int{"ale": 40}, State: sim.StateSleeping}
	snap := &sim.Snapshot{
		Actors:            map[sim.ActorID]*sim.ActorSnapshot{"merchant": subj, "brewer": supplier},
		Structures:        map[sim.StructureID]*sim.Structure{"brewery": {ID: "brewery", DisplayName: "The Brewery"}},
		ItemKinds:         restockCatalog(),
		RestockReorderPct: 25,
	}
	v := buildRestocking(snap, "merchant", subj)
	if v == nil || len(v.Items) != 1 || len(v.Items[0].Vendors) != 1 {
		t.Fatalf("want 1 vendor cue, got %+v", v)
	}
	if !v.Items[0].Vendors[0].ClosedNow {
		t.Error("supplier whose only keeper is asleep should be ClosedNow")
	}
}

// TestBuildRestocking_NotClosedWhenAnotherKeeperAwake: ClosedNow is structure-
// WIDE. The deduped representative is the lowest VendorID, which is arbitrary
// w.r.t. wakefulness — so when the representative ("anders") is asleep but
// ANOTHER keeper of the item at the same structure ("bramble") is awake, the cue
// must NOT be closed. Keying ClosedNow off the representative alone would
// false-close a staffed shop and steer the buyer away from a valid supplier —
// the very failure this fix removes. ZBBS-HOME-406.
func TestBuildRestocking_NotClosedWhenAnotherKeeperAwake(t *testing.T) {
	subj := &sim.ActorSnapshot{Inventory: map[sim.ItemKind]int{"ale": 2}, RestockPolicy: buyPolicy("ale", 20)}
	anders := &sim.ActorSnapshot{WorkStructureID: "brewery", Inventory: map[sim.ItemKind]int{"ale": 40}, State: sim.StateSleeping}
	bramble := &sim.ActorSnapshot{WorkStructureID: "brewery", Inventory: map[sim.ItemKind]int{"ale": 40}, State: sim.StateIdle}
	snap := &sim.Snapshot{
		Actors:            map[sim.ActorID]*sim.ActorSnapshot{"merchant": subj, "anders": anders, "bramble": bramble},
		Structures:        map[sim.StructureID]*sim.Structure{"brewery": {ID: "brewery", DisplayName: "The Brewery"}},
		ItemKinds:         restockCatalog(),
		RestockReorderPct: 25,
	}
	v := buildRestocking(snap, "merchant", subj)
	if v == nil || len(v.Items) != 1 || len(v.Items[0].Vendors) != 1 {
		t.Fatalf("want 1 deduped vendor cue, got %+v", v)
	}
	if v.Items[0].Vendors[0].ClosedNow {
		t.Error("structure with an awake keeper of the item should NOT be ClosedNow")
	}
}

// TestBuildRestocking_ClosedSupplierDemoted: a closed supplier (every vendor of
// the item asleep) sorts BELOW an open one even when its name sorts first
// alphabetically — open-before-closed is the primary sort key (mirrors the
// satiation buy menu), so the restock cue leads with a supplier that can sell.
func TestBuildRestocking_ClosedSupplierDemoted(t *testing.T) {
	subj := &sim.ActorSnapshot{Inventory: map[sim.ItemKind]int{"ale": 2}, RestockPolicy: buyPolicy("ale", 20)}
	amos := &sim.ActorSnapshot{WorkStructureID: "abbey", Inventory: map[sim.ItemKind]int{"ale": 40}, State: sim.StateSleeping} // closed; "Abbey" sorts first
	bram := &sim.ActorSnapshot{WorkStructureID: "brewery", Inventory: map[sim.ItemKind]int{"ale": 40}, State: sim.StateIdle}   // open
	snap := &sim.Snapshot{
		Actors: map[sim.ActorID]*sim.ActorSnapshot{"merchant": subj, "amos": amos, "bram": bram},
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
	if vds[0].StructureID != "brewery" || vds[0].ClosedNow {
		t.Errorf("open supplier must lead the alphabetically-earlier closed one, got first = %+v", vds[0])
	}
	if vds[1].StructureID != "abbey" || !vds[1].ClosedNow {
		t.Errorf("closed supplier must be demoted to the tail, got second = %+v", vds[1])
	}
}

// TestRenderRestocking_ClosedNowPreferredOverShut: when both the live ClosedNow
// and the stale Shut memory point at the same supplier, render emits the
// present-tense "(currently closed)" name marker and NOT the decaying "found it
// shut" recollection — live state beats stale memory (mirrors satiation, HOME-387/406).
func TestRenderRestocking_ClosedNowPreferredOverShut(t *testing.T) {
	v := &RestockingView{Items: []RestockItemView{
		{
			ItemLabel: "ale", CurrentQty: 2, Cap: 20,
			Vendors: []RestockVendor{{StructureLabel: "The Brewery", StructureID: "brewery", Shut: true, ClosedNow: true}},
		},
	}}
	var b strings.Builder
	renderRestocking(&b, v)
	out := b.String()
	if !strings.Contains(out, "The Brewery"+closedNowMarker) {
		t.Errorf("expected the live (currently closed) name marker:\n%s", out)
	}
	if strings.Contains(out, closedBusinessAnnotation) {
		t.Errorf("live ClosedNow should suppress the stale Shut annotation:\n%s", out)
	}
}

// --- LLM-10: offer anchor + purse reserve nudge ----------------------------

// TestBuildRestocking_FillCostFromBundleRatio (LLM-10): FillCost is the headroom
// priced at the last-paid bundle ratio (rounded), not a floored unit price; and a
// single-buy-item policy reports HasOtherBuyGoods=false.
func TestBuildRestocking_FillCostFromBundleRatio(t *testing.T) {
	// Coins comfortably cover the headroom (so the anchor, not the HOME-459 fact,
	// is the relevant path). ale 2/20 → headroom 18; last paid 5 coins for 2 ale →
	// rate 2.5/unit → fill 18 ≈ 45 coins (NOT 18*floor(5/2)=36).
	subj := &sim.ActorSnapshot{Coins: 1000, Inventory: map[sim.ItemKind]int{"ale": 2}, RestockPolicy: buyPolicy("ale", 20)}
	pb := sim.NewRingBuffer[sim.PriceObservation](4)
	pb.Push(sim.PriceObservation{BuyerID: "merchant", Amount: 5, Qty: 2, Consumers: 1, At: time.Now().UTC()})
	snap := &sim.Snapshot{
		Actors:    map[sim.ActorID]*sim.ActorSnapshot{"merchant": subj},
		ItemKinds: restockCatalog(),
		PriceBook: map[sim.PriceBookKey]*sim.RingBuffer[sim.PriceObservation]{
			{SellerID: "brewer", Item: "ale"}: pb,
		},
		RestockReorderPct: 25,
	}
	v := buildRestocking(snap, "merchant", subj)
	if v == nil || len(v.Items) != 1 {
		t.Fatalf("want 1 item, got %+v", v)
	}
	if got := v.Items[0].FillCost; got != 45 {
		t.Errorf("FillCost = %d, want 45 (18 at the 5-for-2 bundle rate, rounded)", got)
	}
	if v.HasOtherBuyGoods {
		t.Error("single-buy-item policy should report HasOtherBuyGoods=false")
	}
}

// TestBuildRestocking_HasOtherBuyGoods: a policy with more than one `buy` entry
// reports HasOtherBuyGoods=true (so the reserve nudge can name "your other goods").
func TestBuildRestocking_HasOtherBuyGoods(t *testing.T) {
	subj := &sim.ActorSnapshot{
		Inventory: map[sim.ItemKind]int{"ale": 1, "salt": 8},
		RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: "ale", Source: sim.RestockSourceBuy, Max: 20},
			{Item: "salt", Source: sim.RestockSourceBuy, Max: 20},
		}},
	}
	snap := &sim.Snapshot{
		Actors:            map[sim.ActorID]*sim.ActorSnapshot{"merchant": subj},
		ItemKinds:         restockCatalog(),
		RestockReorderPct: 25,
	}
	v := buildRestocking(snap, "merchant", subj)
	if v == nil {
		t.Fatal("want a view")
	}
	if !v.HasOtherBuyGoods {
		t.Error("multi-buy-item policy should report HasOtherBuyGoods=true")
	}
}

// TestRenderRestocking_OfferAnchor (LLM-10 A): when the purse comfortably covers
// the headroom and a fill cost is known, render anchors the offer to the fair fill
// cost and invites a lower offer — carrying no ask/price/cost token (HOME-386).
func TestRenderRestocking_OfferAnchor(t *testing.T) {
	var b strings.Builder
	// headroom 15; AffordableQty 18 (> headroom, so coins don't bind → not the 459
	// path); FillCost 40 (< half of 150, so the reserve nudge stays silent).
	renderRestocking(&b, &RestockingView{
		BuyerCoins: 150,
		Items:      []RestockItemView{{ItemLabel: "milk", CurrentQty: 4, Cap: 19, AffordableQty: 18, FillCost: 40}},
	})
	out := b.String()
	if !strings.Contains(out, "Filling to cap is about 40 coins at what you last paid, though you might offer less and see if they take it.") {
		t.Errorf("missing offer anchor + haggle line:\n%s", out)
	}
	if strings.Contains(out, "cover about") {
		t.Errorf("coins covering the headroom should not render the HOME-459 fact:\n%s", out)
	}
	if strings.Contains(out, "buy fewer") {
		t.Errorf("a small fill (< trigger %% of purse) should not render the reserve nudge:\n%s", out)
	}
	lower := strings.ToLower(out)
	if strings.Contains(lower, "ask") || strings.Contains(lower, "price") || strings.Contains(lower, "cost") {
		t.Errorf("anchor must carry no ask/price/cost token (HOME-386):\n%s", out)
	}
}

// TestRenderRestocking_ReserveNudge (LLM-10 B): a fill that would take a big share
// of the purse adds the partial-buy nudge, naming "your other goods" only when the
// reseller has them, and stays silent for a cheap fill.
func TestRenderRestocking_ReserveNudge(t *testing.T) {
	// Big share of purse (120 of 150 = 80% >= 50%) + other goods → full nudge.
	var other strings.Builder
	renderRestocking(&other, &RestockingView{
		BuyerCoins:       150,
		HasOtherBuyGoods: true,
		Items:            []RestockItemView{{ItemLabel: "milk", CurrentQty: 4, Cap: 19, AffordableQty: 18, FillCost: 120}},
	})
	if out := other.String(); !strings.Contains(out, "That is a big share of your 150 coins — buy fewer to keep some back for your other goods.") {
		t.Errorf("missing reserve nudge with other-goods wording:\n%s", out)
	}

	// Big share of purse but no other buy goods → generic "keep some coins back".
	var solo strings.Builder
	renderRestocking(&solo, &RestockingView{
		BuyerCoins:       150,
		HasOtherBuyGoods: false,
		Items:            []RestockItemView{{ItemLabel: "milk", CurrentQty: 4, Cap: 19, AffordableQty: 18, FillCost: 120}},
	})
	if out := solo.String(); !strings.Contains(out, "buy fewer to keep some coins back.") {
		t.Errorf("missing generic reserve nudge:\n%s", out)
	}

	// Below the trigger (60 of 150 = 40% < 50%) → no nudge.
	var cheap strings.Builder
	renderRestocking(&cheap, &RestockingView{
		BuyerCoins:       150,
		HasOtherBuyGoods: true,
		Items:            []RestockItemView{{ItemLabel: "milk", CurrentQty: 4, Cap: 19, AffordableQty: 18, FillCost: 60}},
	})
	if out := cheap.String(); strings.Contains(out, "buy fewer") {
		t.Errorf("a fill below the reserve trigger should add no nudge:\n%s", out)
	}
}

// TestRenderRestocking_AnchorSuppressedWhenCoinsBind: when coins bind before the
// cap, the HOME-459 fact renders and the LLM-10 anchor/reserve lines do NOT (they
// are the coins-comfortably-cover-it case).
func TestRenderRestocking_AnchorSuppressedWhenCoinsBind(t *testing.T) {
	var b strings.Builder
	// headroom 15; AffordableQty 6 (< headroom) → coins bind → 459 path.
	renderRestocking(&b, &RestockingView{
		BuyerCoins: 50,
		Items:      []RestockItemView{{ItemLabel: "milk", CurrentQty: 4, Cap: 19, AffordableQty: 6, FillCost: 120}},
	})
	out := b.String()
	if !strings.Contains(out, "Your 50 coins cover about 6 at what you last paid.") {
		t.Errorf("binding-purse fact should render:\n%s", out)
	}
	if strings.Contains(out, "Filling to cap") || strings.Contains(out, "buy fewer") {
		t.Errorf("anchor/reserve lines should be suppressed when coins bind:\n%s", out)
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
