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

// producePolicy is a one-entry produce policy for `item` at `cap`. A supplier in
// these restock tests is a first-hand PRODUCER of what it sells (a brewery makes
// its ale), which is the LLM-252 precondition for qualifying as a restock supplier
// (isRestockSupplierOf) — a vendor merely holding stock from a past `buy` no
// longer counts.
func producePolicy(item sim.ItemKind, cap int) *sim.RestockPolicy {
	return &sim.RestockPolicy{Restock: []sim.RestockEntry{
		{Item: item, Source: sim.RestockSourceProduce, Max: cap},
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

// TestBuildRestocking_LowStockNoVendorOmitted — LLM-216. A low `buy` item with no
// supplier anywhere (nobody sells it) and no co-present seller has no actionable
// buy path, so buildRestocking omits it rather than surfacing a dead-end capacity
// line the weak model would fixate on. With the only low item omitted, the whole
// section is nil.
func TestBuildRestocking_LowStockNoVendorOmitted(t *testing.T) {
	subj := &sim.ActorSnapshot{Inventory: map[sim.ItemKind]int{"ale": 3}, RestockPolicy: buyPolicy("ale", 20)} // 15%
	snap := &sim.Snapshot{
		Actors:            map[sim.ActorID]*sim.ActorSnapshot{"merchant": subj},
		ItemKinds:         restockCatalog(),
		RestockReorderPct: 25,
	}
	if v := buildRestocking(snap, "merchant", subj); v != nil {
		t.Errorf("want nil — a low item with no actionable supplier is omitted, got %+v", v)
	}
}

func TestBuildRestocking_VendorResolved(t *testing.T) {
	subj := &sim.ActorSnapshot{Coins: 20, Inventory: map[sim.ItemKind]int{"ale": 2}, RestockPolicy: buyPolicy("ale", 20)}
	supplier := &sim.ActorSnapshot{WorkStructureID: "brewery", Inventory: map[sim.ItemKind]int{"ale": 40}, RestockPolicy: producePolicy("ale", 40)}
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
		Coins:           20,
		Inventory:       map[sim.ItemKind]int{"ale": 1},
		RestockPolicy:   buyPolicy("ale", 20),
		CurrentHuddleID: "h1",
	}
	supplier := &sim.ActorSnapshot{
		DisplayName:     "Anders Brewer",
		RestockPolicy:   producePolicy("ale", 40),
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
		Coins:           20,
		Inventory:       map[sim.ItemKind]int{"ale": 1},
		RestockPolicy:   buyPolicy("ale", 20),
		CurrentHuddleID: "h1",
	}
	supplier := &sim.ActorSnapshot{
		DisplayName:     "Anders Brewer",
		RestockPolicy:   producePolicy("ale", 40),
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

// TestBuildRestocking_CoPresentSeller_SameStructureNoHuddle: on the arrival tick at
// a quiet shop there is NO huddle yet (one forms only when someone speaks), but the
// keeper standing inside the same structure is co-present — pay_with_item bootstraps
// the co-located huddle on the call (withHuddleBootstrap, ZBBS-HOME-400). So a seller
// sharing the reseller's structure scope, with neither in a huddle, is surfaced as
// CoPresentSeller. This is the LLM-286 fix: the huddle-only gate could not fire at
// the moment the buy-here imperative was built for.
func TestBuildRestocking_CoPresentSeller_SameStructureNoHuddle(t *testing.T) {
	subj := &sim.ActorSnapshot{
		Inventory:         map[sim.ItemKind]int{"ale": 1},
		RestockPolicy:     buyPolicy("ale", 20),
		InsideStructureID: "brewery", // arrived inside; no huddle yet
	}
	supplier := &sim.ActorSnapshot{
		DisplayName:       "Anders Brewer",
		RestockPolicy:     producePolicy("ale", 40),
		WorkStructureID:   "brewery",
		InsideStructureID: "brewery", // working inside, same scope as the buyer
		Inventory:         map[sim.ItemKind]int{"ale": 40},
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
		t.Errorf("CoPresentSeller = %q, want 'Anders Brewer' (same structure scope, no huddle)", v.Items[0].CoPresentSeller)
	}
}

// TestBuildRestocking_CoPresentSeller_LoiterPinNoHuddle: the live incident (LLM-286).
// An owner-only shop (the Blacksmith) is never entered by customers — the keeper works
// inside while the buyer conducts commerce from the loiter pin OUTSIDE
// (InsideStructureID == ""). The buyer's conversational scope resolves to the shop via
// its loiter pin (conversationalScopeStructure → ResolveLoiteringObject within
// AudienceScopeTiles), matching where pay_with_item's bootstrap forms the huddle. With
// the keeper inside and neither in a huddle, the seller is co-present.
func TestBuildRestocking_CoPresentSeller_LoiterPinNoHuddle(t *testing.T) {
	zero := 0
	pin := sim.WorldPos{X: 100, Y: 100}
	subj := &sim.ActorSnapshot{
		Inventory:     map[sim.ItemKind]int{"ale": 1},
		RestockPolicy: buyPolicy("ale", 20),
		Pos:           pin.Tile(), // loitering at the shop's pin, NOT inside it
	}
	supplier := &sim.ActorSnapshot{
		DisplayName:       "Anders Brewer",
		RestockPolicy:     producePolicy("ale", 40),
		WorkStructureID:   "brewery",
		InsideStructureID: "brewery", // keeper works inside the owner-only shop
		Inventory:         map[sim.ItemKind]int{"ale": 40},
	}
	snap := &sim.Snapshot{
		Actors:     map[sim.ActorID]*sim.ActorSnapshot{"merchant": subj, "brewer": supplier},
		Structures: map[sim.StructureID]*sim.Structure{"brewery": {ID: "brewery", DisplayName: "The Brewery"}},
		// The shop is a village_object (shared-identity bridge) with a zero loiter
		// offset, so its pin sits on the anchor tile the buyer stands on.
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			"brewery": {ID: "brewery", DisplayName: "The Brewery", Pos: pin, LoiterOffsetX: &zero, LoiterOffsetY: &zero},
		},
		Assets:            emptyAssetSet,
		ItemKinds:         restockCatalog(),
		RestockReorderPct: 25,
	}
	v := buildRestocking(snap, "merchant", subj)
	if v == nil || len(v.Items) != 1 {
		t.Fatalf("want 1 item, got %+v", v)
	}
	if v.Items[0].CoPresentSeller != "Anders Brewer" {
		t.Errorf("CoPresentSeller = %q, want 'Anders Brewer' (loiter-pin scope, no huddle)", v.Items[0].CoPresentSeller)
	}
}

// TestBuildRestocking_LoiteringSellerNotCoPresent: the faithful-negative guard for the
// seller predicate. EnsureColocatedHuddle pulls co-located actors into the buyer's
// huddle by literal InsideStructureID == scope, so a seller merely LOITERING at the
// same stall (InsideStructureID == "") is NOT one pay_with_item's bootstrap would
// huddle — surfacing it as co-present would lure a "buy it now" the tool then rejects.
// Both parties at the shop's pin, neither inside, no huddle ⇒ not co-present; the
// walk-to vendor cue still resolves.
func TestBuildRestocking_LoiteringSellerNotCoPresent(t *testing.T) {
	zero := 0
	pin := sim.WorldPos{X: 100, Y: 100}
	subj := &sim.ActorSnapshot{
		Coins:         20,
		Inventory:     map[sim.ItemKind]int{"ale": 1},
		RestockPolicy: buyPolicy("ale", 20),
		Pos:           pin.Tile(),
	}
	supplier := &sim.ActorSnapshot{
		DisplayName:     "Anders Brewer",
		RestockPolicy:   producePolicy("ale", 40),
		WorkStructureID: "brewery",
		Pos:             pin.Tile(), // also loitering at the pin, NOT inside
		Inventory:       map[sim.ItemKind]int{"ale": 40},
	}
	snap := &sim.Snapshot{
		Actors:     map[sim.ActorID]*sim.ActorSnapshot{"merchant": subj, "brewer": supplier},
		Structures: map[sim.StructureID]*sim.Structure{"brewery": {ID: "brewery", DisplayName: "The Brewery"}},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			"brewery": {ID: "brewery", DisplayName: "The Brewery", Pos: pin, LoiterOffsetX: &zero, LoiterOffsetY: &zero},
		},
		Assets:            emptyAssetSet,
		ItemKinds:         restockCatalog(),
		RestockReorderPct: 25,
	}
	v := buildRestocking(snap, "merchant", subj)
	if v == nil || len(v.Items) != 1 {
		t.Fatalf("want 1 item, got %+v", v)
	}
	if v.Items[0].CoPresentSeller != "" {
		t.Errorf("CoPresentSeller = %q, want empty (seller only loitering, not inside)", v.Items[0].CoPresentSeller)
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
	// ZBBS-HOME-388 wanted the trade visible as a speech bubble and named the speak
	// TOOL for it. LLM-321 later made speak terminal alongside pay_with_item, so
	// "call pay_with_item … then also use speak" could never be obeyed: the offer
	// ended the tick and the speak was skipped, or the speak ended it and no offer
	// was ever made. The handoff line rides in pay_with_item's own `say` now, which
	// lands on the same utterance path — the bubble still spawns, from one call.
	if !strings.Contains(out, "call pay_with_item") || !strings.Contains(out, "your handoff line in say") {
		t.Errorf("buy-here imperative should carry the handoff line in say:\n%s", out)
	}
	if strings.Contains(out, "Then also use speak") {
		t.Errorf("buy-here imperative asks for a second terminal verb (LLM-350):\n%s", out)
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
		Coins:           20,
		Inventory:       map[sim.ItemKind]int{"ale": 1},
		RestockPolicy:   buyPolicy("ale", 20),
		CurrentHuddleID: "h1",
	}
	supplier := &sim.ActorSnapshot{
		DisplayName:     "Anders Brewer",
		RestockPolicy:   producePolicy("ale", 40),
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
		Coins:           20,
		Inventory:       map[sim.ItemKind]int{"ale": 1},
		RestockPolicy:   buyPolicy("ale", 20),
		CurrentHuddleID: "h1",
	}
	supplier := &sim.ActorSnapshot{
		DisplayName:     "Anders Brewer",
		RestockPolicy:   producePolicy("ale", 40),
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
		Coins:           20,
		Inventory:       map[sim.ItemKind]int{"ale": 1},
		RestockPolicy:   buyPolicy("ale", 20),
		CurrentHuddleID: "h1",
	}
	ann := &sim.ActorSnapshot{DisplayName: "Ann", WorkStructureID: "brewery", Inventory: map[sim.ItemKind]int{"ale": 40}, CurrentHuddleID: "h1", RestockPolicy: producePolicy("ale", 40)}
	zed := &sim.ActorSnapshot{DisplayName: "Zed", WorkStructureID: "brewery", Inventory: map[sim.ItemKind]int{"ale": 40}, CurrentHuddleID: "h1", RestockPolicy: producePolicy("ale", 40)}
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
// unresolvable-structure suppliers are all excluded. Asserted on findItemVendors
// directly — with every supplier excluded, buildRestocking would omit the whole
// item (LLM-216: no actionable buy path), so the vendor-resolution result is what
// this test is really about.
func TestBuildRestocking_VendorExclusions(t *testing.T) {
	subj := &sim.ActorSnapshot{Coins: 20, Inventory: map[sim.ItemKind]int{"ale": 1}, RestockPolicy: buyPolicy("ale", 20)}
	// PC holding ale — excluded (PCs don't sell through the NPC commerce path).
	// All three carry a produce policy so the ONLY reason they drop is the
	// PC / no-workplace / ghost-structure rule under test, not the LLM-252
	// first-hand-supplier gate.
	pcSeller := &sim.ActorSnapshot{Kind: sim.KindPC, WorkStructureID: "brewery", Inventory: map[sim.ItemKind]int{"ale": 40}, RestockPolicy: producePolicy("ale", 40)}
	// No workplace — excluded.
	noWork := &sim.ActorSnapshot{Inventory: map[sim.ItemKind]int{"ale": 40}, RestockPolicy: producePolicy("ale", 40)}
	// Workplace not in snapshot.Structures — excluded (unactionable destination).
	ghost := &sim.ActorSnapshot{WorkStructureID: "nowhere", Inventory: map[sim.ItemKind]int{"ale": 40}, RestockPolicy: producePolicy("ale", 40)}
	snap := &sim.Snapshot{
		Actors: map[sim.ActorID]*sim.ActorSnapshot{
			"merchant": subj, "pc": pcSeller, "drifter": noWork, "ghost": ghost,
		},
		Structures:        map[sim.StructureID]*sim.Structure{"brewery": {ID: "brewery", DisplayName: "The Brewery"}},
		ItemKinds:         restockCatalog(),
		RestockReorderPct: 25,
	}
	if vds, _ := findItemVendors(snap, "merchant", subj, "ale"); len(vds) != 0 {
		t.Errorf("all suppliers should be excluded, got %+v", vds)
	}
	// With no resolvable supplier and no co-present seller, the item is omitted.
	if v := buildRestocking(snap, "merchant", subj); v != nil {
		t.Errorf("want nil — no actionable supplier means the item is omitted, got %+v", v)
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
	subj := &sim.ActorSnapshot{Coins: 20, Inventory: map[sim.ItemKind]int{"ale": 1}, RestockPolicy: buyPolicy("ale", 20)}
	// Two brewers at the same structure; "anders" (< "bramble") is the rep.
	anders := &sim.ActorSnapshot{WorkStructureID: "brewery", Inventory: map[sim.ItemKind]int{"ale": 40}, RestockPolicy: producePolicy("ale", 40)}
	bramble := &sim.ActorSnapshot{WorkStructureID: "brewery", Inventory: map[sim.ItemKind]int{"ale": 40}, RestockPolicy: producePolicy("ale", 40)}
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
	subj := &sim.ActorSnapshot{Coins: 20, Inventory: map[sim.ItemKind]int{"ale": 1}, RestockPolicy: buyPolicy("ale", 20)}
	supplier := &sim.ActorSnapshot{WorkStructureID: "brewery", Inventory: map[sim.ItemKind]int{"ale": 40}, RestockPolicy: producePolicy("ale", 40)}
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
	supplier := &sim.ActorSnapshot{WorkStructureID: "brewery", Inventory: map[sim.ItemKind]int{"ale": 40}, RestockPolicy: producePolicy("ale", 40)}
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
	// One resolvable, affordable supplier (anders at 10/unit ≤ 100 coins) so the item
	// has an actionable buy path and survives; AffordableQty reads the PriceBook, not
	// the vendor list, so the timestamp-tie determinism is unaffected.
	anders := &sim.ActorSnapshot{WorkStructureID: "abbey", Inventory: map[sim.ItemKind]int{"ale": 40}, RestockPolicy: producePolicy("ale", 40)}
	snap := &sim.Snapshot{
		Actors:     map[sim.ActorID]*sim.ActorSnapshot{"merchant": subj, "anders": anders},
		Structures: map[sim.StructureID]*sim.Structure{"abbey": {ID: "abbey", DisplayName: "Abbey Brewhouse"}},
		ItemKinds:  restockCatalog(),
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
	if !strings.Contains(out, "buy from The Brewery (destination: brewery), ~2 coins") {
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
	if !strings.Contains(out, "buy from Ellis Farm (destination: ellis)") {
		t.Errorf("missing vendor destination line:\n%s", out)
	}
	// Empty CostText must not render a trailing ", <cost>" clause after the id.
	if strings.Contains(out, "(destination: ellis),") {
		t.Errorf("empty CostText should not render a trailing cost clause:\n%s", out)
	}
}

// --- experiential remembered-shut drop (LLM-216) ----------------------

// TestBuildRestocking_DropsRememberedShutSupplier — LLM-216. A supplier the buyer
// remembers finding shut (a decaying ObservedClosed memory) is DROPPED from the
// walk-to list, mirroring the seek-work directory. The old "annotate, don't demote"
// posture (LLM-126) left the weak model touring the dead ends (Josiah's every-tick
// move_to loop among shut farms). An open supplier the buyer has never visited
// survives (unknown price → no affordability skip). With the shut Abbey gone, only
// the open Brewery remains.
func TestBuildRestocking_DropsRememberedShutSupplier(t *testing.T) {
	now := time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC)
	subj := &sim.ActorSnapshot{
		Coins:         20, // not broke; prices are unknown anyway, so affordability isn't in play
		Inventory:     map[sim.ItemKind]int{"ale": 2},
		RestockPolicy: buyPolicy("ale", 20),
		// He remembers the Abbey (alphabetically first) shut; the Brewery he does not.
		Observed: sim.NewObservedStates(map[sim.ObservedStateKey]time.Time{
			{StructureID: "abbey", Condition: sim.ObservedClosed}: now.Add(-time.Hour),
		}),
	}
	amos := &sim.ActorSnapshot{WorkStructureID: "abbey", Inventory: map[sim.ItemKind]int{"ale": 40}, State: sim.StateIdle, RestockPolicy: producePolicy("ale", 40)}
	bram := &sim.ActorSnapshot{WorkStructureID: "brewery", Inventory: map[sim.ItemKind]int{"ale": 40}, State: sim.StateIdle, RestockPolicy: producePolicy("ale", 40)}
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
	if v == nil || len(v.Items) != 1 || len(v.Items[0].Vendors) != 1 {
		t.Fatalf("want the shut Abbey dropped and only the open Brewery left, got %+v", v)
	}
	if vd := v.Items[0].Vendors[0]; vd.StructureID != "brewery" {
		t.Errorf("surviving supplier should be the open Brewery, got %+v", vd)
	}
}

// TestBuildRestocking_MeansToPaySupplier — LLM-216 as amended by LLM-406. The gate on
// a walk-to supplier is MEANS TO PAY, not coins: pay_with_item settles in goods as
// readily as coin, so the question is "can you pay at all", not "can you pay in coin".
//
//   - Purse covers the remembered price → an ordinary coin buy.
//   - Purse can't cover it, but the pack holds other goods → the supplier SURVIVES,
//     flagged Barter, and render steers to a goods offer. This is the live Josiah
//     Thorne case, and the coins-only test that used to drop it is what turned an
//     illiquid distributor into an absorbing state.
//   - Neither coin nor goods → a real payment dead-end. No destination — but the
//     supplier is NAMED as blocked with its reason, rather than the item vanishing and
//     the whole section rendering nil (the silence LLM-406 removes).
//
// A good is not payment for itself, so the ale on the shelf never counts as means to
// buy ale — only the skillet does.
func TestBuildRestocking_MeansToPaySupplier(t *testing.T) {
	priced := func() *sim.RingBuffer[sim.PriceObservation] {
		buf := sim.NewRingBuffer[sim.PriceObservation](8)
		// He last paid this seller 6 coins for 1 ale.
		buf.Push(sim.PriceObservation{BuyerID: "merchant", Amount: 6, Qty: 1, Consumers: 1, At: time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)})
		return buf
	}
	mk := func(coins int, withPrice, withGoods bool) *sim.Snapshot {
		inv := map[sim.ItemKind]int{"ale": 2} // the 2 ale on the shelf are what he's restocking — never his means
		if withGoods {
			inv["skillet"] = 1 // something else in the pack, and so something to trade
		}
		subj := &sim.ActorSnapshot{Coins: coins, Inventory: inv, RestockPolicy: buyPolicy("ale", 20)}
		supplier := &sim.ActorSnapshot{WorkStructureID: "brewery", Inventory: map[sim.ItemKind]int{"ale": 40}, RestockPolicy: producePolicy("ale", 40)}
		snap := &sim.Snapshot{
			PublishedAt:       time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC),
			Actors:            map[sim.ActorID]*sim.ActorSnapshot{"merchant": subj, "brewer": supplier},
			Structures:        map[sim.StructureID]*sim.Structure{"brewery": {ID: "brewery", DisplayName: "The Brewery"}},
			ItemKinds:         restockCatalog(),
			RestockReorderPct: 25,
		}
		if withPrice {
			snap.PriceBook = map[sim.PriceBookKey]*sim.RingBuffer[sim.PriceObservation]{
				{SellerID: "brewer", Item: "ale"}: priced(),
			}
		}
		return snap
	}
	blockedFor := func(t *testing.T, snap *sim.Snapshot, why string) {
		t.Helper()
		v := buildRestocking(snap, "merchant", snap.Actors["merchant"])
		if v == nil || len(v.Items) != 1 {
			t.Fatalf("%s: the item should still render, named as blocked, got %+v", why, v)
		}
		it := v.Items[0]
		if len(it.Vendors) != 0 {
			t.Fatalf("%s: an unpayable supplier is not a destination, got %+v", why, it.Vendors)
		}
		if len(it.Blocked) != 1 || it.Blocked[0].Reason != restockBlockNoMeans || it.Blocked[0].StructureLabel != "The Brewery" {
			t.Fatalf("%s: want The Brewery named blocked-no-means, got %+v", why, it.Blocked)
		}
	}

	// Purse covers the price (6 >= 6) → an ordinary coin buy, no barter steer.
	flush := mk(6, true, false)
	v := buildRestocking(flush, "merchant", flush.Actors["merchant"])
	if v == nil || len(v.Items) != 1 || len(v.Items[0].Vendors) != 1 {
		t.Fatalf("a keeper who can cover the remembered price keeps the supplier, got %+v", v)
	}
	if v.Items[0].Vendors[0].Barter {
		t.Error("a keeper paying in coin should not be steered to barter")
	}

	// LLM-406: the purse can't cover the price, but the pack can. The supplier survives,
	// steered to a goods offer — the fix for the live distributor deadlock.
	bartering := mk(0, true, true)
	v = buildRestocking(bartering, "merchant", bartering.Actors["merchant"])
	if v == nil || len(v.Items) != 1 || len(v.Items[0].Vendors) != 1 {
		t.Fatalf("a coin-poor keeper holding goods keeps the supplier (barter), got %+v", v)
	}
	if !v.Items[0].Vendors[0].Barter {
		t.Error("a supplier the coins can't cover but the goods can should be flagged Barter")
	}
	// The rendered line must offer the goods payment, not a coin price he cannot meet.
	var b strings.Builder
	renderRestocking(&b, v)
	if out := b.String(); !strings.Contains(out, "offer goods you carry in trade") {
		t.Errorf("a barter supplier should be steered to a goods offer:\n%s", out)
	}

	// Broke with an empty pack, at a known price above the purse → nothing to pay with.
	blockedFor(t, mk(0, true, false), "broke, known price")
	// ...and at an UNKNOWN price too: walk-and-learn is only worth the trip if he could
	// settle on arrival, and with neither coin nor goods he could not.
	blockedFor(t, mk(0, false, false), "broke, unknown price")

	// A penniless keeper who still has goods CAN walk over and learn an unknown price —
	// he has something to settle with when he gets there.
	brokeWithGoods := mk(0, false, true)
	v = buildRestocking(brokeWithGoods, "merchant", brokeWithGoods.Actors["merchant"])
	if v == nil || len(v.Items) != 1 || len(v.Items[0].Vendors) != 1 {
		t.Fatalf("a penniless keeper holding goods keeps an unknown-price supplier, got %+v", v)
	}
}

// TestRenderBlockedItem_CodaMatchesTheReasons — LLM-406. The blocked scene must
// self-resolve for EVERY reason it names (LLM-298: a want with no outlet is what makes
// a weak model invent an errand). An item blocked both ways — one supplier shut, another
// beyond any means — needs both resolutions: keying the coda off one reason alone
// silently drops the other supplier's way out (code_review). And no blocked line, in any
// combination, may ever carry a "(destination: …)" token — that is what the model echoes
// into move_to, and these are precisely the places it must not go.
func TestRenderBlockedItem_CodaMatchesTheReasons(t *testing.T) {
	item := func(blocked ...RestockBlockedSupplier) RestockItemView {
		return RestockItemView{ItemLabel: "milk", CurrentQty: 0, Cap: 12, Blocked: blocked, kind: "milk"}
	}
	shut := RestockBlockedSupplier{StructureLabel: "James Farm", Reason: restockBlockShut}
	noMeans := RestockBlockedSupplier{StructureLabel: "Ellis Farm", Reason: restockBlockNoMeans}

	cases := []struct {
		name    string
		it      RestockItemView
		want    []string
		exclude []string
	}{
		{
			name: "shut only — the supplier reopens, so try again later",
			it:   item(shut),
			want: []string{"James Farm", "found it shut", "Look in again another day"},
		},
		{
			name: "no means only — nothing to pay with, so wait for trade to come in",
			it:   item(noMeans),
			want: []string{"Ellis Farm", "neither the coin", "take what trade comes to you"},
			// The shut resolution would be a lie here: Ellis is open, and looking in again
			// changes nothing while the purse and the pack are both empty.
			exclude: []string{"Look in again another day"},
		},
		{
			name: "both — each supplier's reason AND each way out",
			it:   item(noMeans, shut),
			want: []string{
				"Ellis Farm", "neither the coin",
				"James Farm", "found it shut",
				"take what trade comes to you", "looking in on another day",
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var b strings.Builder
			renderBlockedItem(&b, c.it)
			out := b.String()
			for _, w := range c.want {
				if !strings.Contains(out, w) {
					t.Errorf("blocked scene missing %q:\n%s", w, out)
				}
			}
			for _, x := range c.exclude {
				if strings.Contains(out, x) {
					t.Errorf("blocked scene should not carry %q:\n%s", x, out)
				}
			}
			if strings.Contains(out, "destination:") {
				t.Errorf("a blocked supplier must never be rendered as a move_to destination:\n%s", out)
			}
		})
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
	subj := &sim.ActorSnapshot{Coins: 20, Inventory: map[sim.ItemKind]int{"ale": 2}, RestockPolicy: buyPolicy("ale", 20)}
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
	// That supplier as a resolvable, affordable vendor (last paid 7 ≤ 20 coins) so the
	// low item has an actionable buy path and surfaces (LLM-216); the sell-through and
	// cost figures under test read the price book, not the vendor list.
	supplierActor := &sim.ActorSnapshot{WorkStructureID: "supplyDepot", Inventory: map[sim.ItemKind]int{"ale": 40}, RestockPolicy: producePolicy("ale", 40)}
	snap := &sim.Snapshot{
		PublishedAt: now,
		Actors:      map[sim.ActorID]*sim.ActorSnapshot{"merchant": subj, "supplier": supplierActor},
		Structures:  map[sim.StructureID]*sim.Structure{"supplyDepot": {ID: "supplyDepot", DisplayName: "Supply Depot"}},
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
	subj := &sim.ActorSnapshot{Coins: 20, Inventory: map[sim.ItemKind]int{"ale": 2}, RestockPolicy: buyPolicy("ale", 20)}
	pb := sim.NewRingBuffer[sim.PriceObservation](20)
	pb.Push(sim.PriceObservation{BuyerID: "merchant", Amount: 4, Qty: 4, Consumers: 1, At: now.Add(-1 * time.Hour)})
	// The other party as a resolvable, affordable vendor so the low item surfaces
	// (LLM-216); RecentSalesUnits under test reads the merchant-as-SELLER ring, which
	// has no entry here — the point of the test.
	elseActor := &sim.ActorSnapshot{WorkStructureID: "elseStore", Inventory: map[sim.ItemKind]int{"ale": 40}, RestockPolicy: producePolicy("ale", 40)}
	snap := &sim.Snapshot{
		PublishedAt: now,
		Actors:      map[sim.ActorID]*sim.ActorSnapshot{"merchant": subj, "someone_else": elseActor},
		Structures:  map[sim.StructureID]*sim.Structure{"elseStore": {ID: "elseStore", DisplayName: "Else Store"}},
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

// TestBuildRestocking_ResaleUnitAndOverBuying pins the LLM-385 buy-side fields: the
// reseller's own realized resale RATE (the ceiling the buying-in anchor is judged
// against) and the OVER-BUYING flag (bought markedly more than it sold this window).
// Milk: sold 9 units for 12 coins → resale 1.3 rounds to 1; bought 35 units → over-buying
// (35 >= 4 and 35 >= 2×9+1). Coins high so the supplier isn't dropped as unaffordable.
func TestBuildRestocking_ResaleUnitAndOverBuying(t *testing.T) {
	now := time.Date(2026, 6, 23, 12, 0, 0, 0, time.UTC)
	subj := &sim.ActorSnapshot{Coins: 100, Inventory: map[sim.ItemKind]int{"milk": 4}, RestockPolicy: buyPolicy("milk", 20)}
	// merchant-as-SELLER ring for milk (resale rate): 9 units for 12 coins → 1.3 → 1.
	sellPB := sim.NewRingBuffer[sim.PriceObservation](20)
	sellPB.Push(sim.PriceObservation{BuyerID: "cust", Amount: 12, Qty: 9, Consumers: 1, At: now.Add(-2 * 24 * time.Hour)})
	// supplier-as-SELLER ring holding the merchant's in-window PURCHASE of 35 units
	// (bundle price 40 ≤ 100 coins, so the supplier survives the affordability drop).
	buyPB := sim.NewRingBuffer[sim.PriceObservation](20)
	buyPB.Push(sim.PriceObservation{BuyerID: "merchant", Amount: 40, Qty: 35, Consumers: 1, At: now.Add(-1 * 24 * time.Hour)})
	supplierActor := &sim.ActorSnapshot{WorkStructureID: "farm", Inventory: map[sim.ItemKind]int{"milk": 40}, RestockPolicy: producePolicy("milk", 40)}
	snap := &sim.Snapshot{
		PublishedAt: now,
		Actors:      map[sim.ActorID]*sim.ActorSnapshot{"merchant": subj, "supplier": supplierActor},
		Structures:  map[sim.StructureID]*sim.Structure{"farm": {ID: "farm", DisplayName: "Ellis Farm"}},
		ItemKinds:   restockCatalog(),
		PriceBook: map[sim.PriceBookKey]*sim.RingBuffer[sim.PriceObservation]{
			{SellerID: "merchant", Item: "milk"}: sellPB,
			{SellerID: "supplier", Item: "milk"}: buyPB,
		},
		RestockReorderPct: 25,
	}
	v := buildRestocking(snap, "merchant", subj)
	if v == nil || len(v.Items) != 1 {
		t.Fatalf("want 1 item, got %+v", v)
	}
	it := v.Items[0]
	if it.ResaleUnit != 1 {
		t.Errorf("ResaleUnit = %d, want 1 (12 coins / 9 units → 1.3 → 1)", it.ResaleUnit)
	}
	if it.RecentBuyUnits != 35 {
		t.Errorf("RecentBuyUnits = %d, want 35", it.RecentBuyUnits)
	}
	if !it.OverBuying {
		t.Errorf("OverBuying = false, want true (35 bought vs 9 sold)")
	}

	// Boundary: bought 10 against 8 SOLD is NOT over-buying (10 < 2×8+1 = 17) — a
	// healthy shop restocking about what it moves. ResaleUnit rounds its 16/8 = 2.
	subj2 := &sim.ActorSnapshot{Coins: 100, Inventory: map[sim.ItemKind]int{"milk": 4}, RestockPolicy: buyPolicy("milk", 20)}
	sellPB2 := sim.NewRingBuffer[sim.PriceObservation](20)
	sellPB2.Push(sim.PriceObservation{BuyerID: "cust", Amount: 16, Qty: 8, Consumers: 1, At: now.Add(-2 * 24 * time.Hour)})
	buyPB2 := sim.NewRingBuffer[sim.PriceObservation](20)
	buyPB2.Push(sim.PriceObservation{BuyerID: "merchant", Amount: 10, Qty: 10, Consumers: 1, At: now.Add(-1 * 24 * time.Hour)})
	supplier2 := &sim.ActorSnapshot{WorkStructureID: "farm", Inventory: map[sim.ItemKind]int{"milk": 40}, RestockPolicy: producePolicy("milk", 40)}
	snap2 := &sim.Snapshot{
		PublishedAt: now,
		Actors:      map[sim.ActorID]*sim.ActorSnapshot{"merchant": subj2, "supplier": supplier2},
		Structures:  map[sim.StructureID]*sim.Structure{"farm": {ID: "farm", DisplayName: "Ellis Farm"}},
		ItemKinds:   restockCatalog(),
		PriceBook: map[sim.PriceBookKey]*sim.RingBuffer[sim.PriceObservation]{
			{SellerID: "merchant", Item: "milk"}: sellPB2,
			{SellerID: "supplier", Item: "milk"}: buyPB2,
		},
		RestockReorderPct: 25,
	}
	v2 := buildRestocking(snap2, "merchant", subj2)
	if v2 == nil || len(v2.Items) != 1 {
		t.Fatalf("want 1 item (boundary), got %+v", v2)
	}
	if v2.Items[0].OverBuying {
		t.Errorf("OverBuying = true, want false (10 bought vs 8 sold — a modest imbalance)")
	}
	if v2.Items[0].ResaleUnit != 2 {
		t.Errorf("ResaleUnit = %d, want 2 (16 coins / 8 units)", v2.Items[0].ResaleUnit)
	}
}

// TestRenderRestocking_ResaleCeilingAndOverBuying pins the two LLM-385 buy-side clauses:
// the resale-ceiling guard (ResaleUnit > 0) and the over-buying steer (OverBuying), each
// silent when its field is unset.
func TestRenderRestocking_ResaleCeilingAndOverBuying(t *testing.T) {
	var b strings.Builder
	renderRestocking(&b, &RestockingView{Items: []RestockItemView{
		{ItemLabel: "milk", CurrentQty: 4, Cap: 20, ResaleUnit: 1,
			RecentBuyUnits: 35, RecentSalesUnits: 9, OverBuying: true},
	}})
	out := b.String()
	if !strings.Contains(out, "You resell it for about 1 coin each — paying above your resale rate loses coin on each one.") {
		t.Errorf("missing resale-ceiling clause:\n%s", out)
	}
	if !strings.Contains(out, "You've bought about 35 this past week but sold only 9 — you're restocking faster than it sells, so buy sparingly, if at all.") {
		t.Errorf("missing over-buying steer:\n%s", out)
	}

	// Over-buying renders even when nothing sold (a dead good it keeps restocking).
	var dead strings.Builder
	renderRestocking(&dead, &RestockingView{Items: []RestockItemView{
		{ItemLabel: "sage", CurrentQty: 1, Cap: 6, RecentBuyUnits: 6, RecentSalesUnits: 0, OverBuying: true},
	}})
	if !strings.Contains(dead.String(), "You've bought about 6 this past week but sold only 0 —") {
		t.Errorf("over-buying steer should render with zero sales:\n%s", dead.String())
	}

	// Negatives: no resale rate → no ceiling clause; not over-buying → no steer.
	var quiet strings.Builder
	renderRestocking(&quiet, &RestockingView{Items: []RestockItemView{
		{ItemLabel: "milk", CurrentQty: 4, Cap: 20, ResaleUnit: 0, OverBuying: false},
	}})
	q := quiet.String()
	if strings.Contains(q, "You resell it for") {
		t.Errorf("no resale rate should omit the ceiling clause:\n%s", q)
	}
	if strings.Contains(q, "restocking faster than it sells") {
		t.Errorf("not over-buying should omit the steer:\n%s", q)
	}
}

// --- LLM-252: restock supplier must be a first-hand supplier or the distributor ---

// carrotSupplyChainSnap seeds a carrot supply chain: a producer (Moses at the
// farm, produces carrots), a fellow reseller (John at the tavern, holds carrots
// only via a `buy` entry), and the distributor (Josiah at the distributor-tagged
// store, also a `buy`-only holder). `subj` restocks carrots by `buy`. No wholesaler
// tags, so the LLM-252 first-hand-supplier gate is the only thing under test (not
// the wholesale gate). Sellers optionally share the buyer's huddle for the
// co-present cue. No PriceBook → unknown prices → no affordability skip.
func carrotSupplyChainSnap(subj *sim.ActorSnapshot, coPresent bool) *sim.Snapshot {
	huddle := sim.HuddleID("")
	if coPresent {
		huddle = "h1"
		subj.CurrentHuddleID = huddle
	}
	moses := &sim.ActorSnapshot{DisplayName: "Moses", WorkStructureID: "farm", Inventory: map[sim.ItemKind]int{"carrots": 40}, RestockPolicy: producePolicy("carrots", 40), CurrentHuddleID: huddle}
	john := &sim.ActorSnapshot{DisplayName: "John Ellis", WorkStructureID: "tavern", Inventory: map[sim.ItemKind]int{"carrots": 12}, RestockPolicy: buyPolicy("carrots", 12), CurrentHuddleID: huddle}
	josiah := &sim.ActorSnapshot{DisplayName: "Josiah Thorne", WorkStructureID: "store", Inventory: map[sim.ItemKind]int{"carrots": 20}, RestockPolicy: buyPolicy("carrots", 6), CurrentHuddleID: huddle}
	return &sim.Snapshot{
		Actors: map[sim.ActorID]*sim.ActorSnapshot{"subj": subj, "moses": moses, "john": john, "josiah": josiah},
		Structures: map[sim.StructureID]*sim.Structure{
			"farm":   {ID: "farm", DisplayName: "The Farm"},
			"tavern": {ID: "tavern", DisplayName: "The Tavern"},
			"store":  {ID: "store", DisplayName: "General Store"},
		},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			"store": {ID: "store", OwnerActorID: "josiah", Tags: []string{sim.TagDistributor}},
		},
		ItemKinds:         map[sim.ItemKind]*sim.ItemKindDef{"carrots": {Name: "carrots", DisplayLabel: "carrots", Category: sim.ItemCategoryFood}},
		RestockReorderPct: 25,
	}
}

// TestFindItemVendors_ExcludesResellerHoldingBoughtStock — LLM-252. The restock
// directory lists a producer of the item and the distributor as suppliers, but NOT
// a fellow reseller who holds the item only from a past `buy` — the Josiah↔John
// carrot buy-back. The buyer here is a non-distributor, so its suppliers collapse to
// the producing farm and the distributor store; the tavern (a reseller) is gone.
func TestFindItemVendors_ExcludesResellerHoldingBoughtStock(t *testing.T) {
	subj := &sim.ActorSnapshot{Coins: 50, Inventory: map[sim.ItemKind]int{"carrots": 1}, RestockPolicy: buyPolicy("carrots", 12)}
	snap := carrotSupplyChainSnap(subj, false)
	got := map[sim.StructureID]bool{}
	vendors, _ := findItemVendors(snap, "subj", subj, "carrots")
	for _, vd := range vendors {
		got[vd.StructureID] = true
	}
	if !got["farm"] {
		t.Error("the producing farm should be a restock supplier")
	}
	if !got["store"] {
		t.Error("the distributor should be a restock supplier")
	}
	if got["tavern"] {
		t.Error("a reseller holding bought carrots must NOT be a restock supplier (buy-back guard)")
	}
	if len(got) != 2 {
		t.Errorf("want exactly {farm, store}, got %v", got)
	}
}

// TestCoPresentSellerForItem_ExcludesReseller — LLM-252. The co-present buy-here
// imperative (the hard "X is here — buy it now" cue that drove the buy-back) never
// names a fellow reseller: with only John (a carrot reseller) co-present, no seller
// is surfaced; with the producing Moses also co-present, HE is the one named.
func TestCoPresentSellerForItem_ExcludesReseller(t *testing.T) {
	// Only the reseller (John) is co-present → no co-present restock seller.
	subjA := &sim.ActorSnapshot{Coins: 50, Inventory: map[sim.ItemKind]int{"carrots": 1}, RestockPolicy: buyPolicy("carrots", 12)}
	snapA := carrotSupplyChainSnap(subjA, true)
	// Take Moses and Josiah out of the huddle so ONLY the reseller John is co-present.
	snapA.Actors["moses"].CurrentHuddleID = "elsewhere"
	snapA.Actors["josiah"].CurrentHuddleID = "elsewhere"
	if name, id := coPresentSellerForItem(snapA, "subj", subjA, "carrots"); name != "" || id != "" {
		t.Errorf("a co-present reseller must not be surfaced as a restock seller (buy-back guard), got %q/%q", name, id)
	}

	// Producer Moses is co-present → HE is the named co-present seller.
	subjB := &sim.ActorSnapshot{Coins: 50, Inventory: map[sim.ItemKind]int{"carrots": 1}, RestockPolicy: buyPolicy("carrots", 12)}
	snapB := carrotSupplyChainSnap(subjB, true)
	snapB.Actors["josiah"].CurrentHuddleID = "elsewhere"
	if name, id := coPresentSellerForItem(snapB, "subj", subjB, "carrots"); id != "moses" || name != "Moses" {
		t.Errorf("the co-present producer should be the named seller, got %q/%q", name, id)
	}
}

// degradeMosesFarm makes Moses's producing farm a degraded owned business (worn
// past the degrade threshold — shut for refill, still sells its on-hand stock,
// LLM-304) on a carrotSupplyChainSnap. Shared by the two LLM-310 rescope guards.
func degradeMosesFarm(snap *sim.Snapshot) {
	snap.StallWearRepairThreshold = 400
	snap.StallWearDegradeThreshold = 600
	snap.VillageObjects["farm"] = &sim.VillageObject{ID: "farm", OwnerActorID: "moses", Tags: []string{sim.TagBusiness}, Wear: 650}
}

// TestFindItemVendors_DegradedSellerStillListedWhileStocked is the LLM-310 rescope
// guard for the walk-to directory. Degrade blocks REFILL, not selling (LLM-304), so
// a degraded keeper who still holds stock stays a valid buy destination — buyers
// clearing his on-hand shelves is his recovery path. Vendor selection keys on stock
// (qty>0 via eachVendorOffer), never on the seller's wear state, so the sold-empty
// dead-end the original ticket wanted to suppress falls out on its own the moment the
// shelf hits zero. Guards against a future re-add of the dropped degrade-based seller
// exclusion (fix #2), which would starve the recovery loop.
func TestFindItemVendors_DegradedSellerStillListedWhileStocked(t *testing.T) {
	subj := &sim.ActorSnapshot{Coins: 50, Inventory: map[sim.ItemKind]int{"carrots": 1}, RestockPolicy: buyPolicy("carrots", 12)}
	snap := carrotSupplyChainSnap(subj, false)
	degradeMosesFarm(snap)
	listed := func() map[sim.StructureID]bool {
		got := map[sim.StructureID]bool{}
		vendors, _ := findItemVendors(snap, "subj", subj, "carrots")
		for _, vd := range vendors {
			got[vd.StructureID] = true
		}
		return got
	}
	// Degraded but still holding 40 carrots → still a supplier.
	if got := listed(); !got["farm"] {
		t.Errorf("a degraded seller that still holds stock must remain a restock supplier (LLM-304 recovery loop), got %v", got)
	}
	// Sold down to empty → drops out via the qty>0 gate, no degrade check needed.
	snap.Actors["moses"].Inventory["carrots"] = 0
	if got := listed(); got["farm"] {
		t.Errorf("a seller sold down to 0 must drop out via the qty>0 gate, got %v", got)
	}
}

// TestCoPresentSellerForItem_NamesDegradedSellerWhileStocked is the LLM-310 rescope
// guard for the co-present buy-here arm — the "## Restocking" imperative that goaded
// the original Elizabeth↔Josiah loop. Post-LLM-304 a degraded keeper still sells his
// on-hand stock, so while he holds stock he must still be named as the co-present
// seller (naming him is exactly what lets a buyer clear his shelves — his recovery
// path). The helper keys on stock, never on wear; sold empty, he drops out via qty>0.
func TestCoPresentSellerForItem_NamesDegradedSellerWhileStocked(t *testing.T) {
	subj := &sim.ActorSnapshot{Coins: 50, Inventory: map[sim.ItemKind]int{"carrots": 1}, RestockPolicy: buyPolicy("carrots", 12)}
	snap := carrotSupplyChainSnap(subj, true)
	// Only the producer Moses co-present (drop the reseller + distributor from the huddle).
	snap.Actors["john"].CurrentHuddleID = "elsewhere"
	snap.Actors["josiah"].CurrentHuddleID = "elsewhere"
	degradeMosesFarm(snap)
	// Degraded but holding 40 carrots → still the named co-present seller.
	if name, id := coPresentSellerForItem(snap, "subj", subj, "carrots"); id != "moses" || name != "Moses" {
		t.Errorf("a degraded seller that still holds stock must still be named co-present (LLM-304 recovery), got %q/%q", name, id)
	}
	// Sold down to empty → drops out via the qty>0 gate.
	snap.Actors["moses"].Inventory["carrots"] = 0
	if name, id := coPresentSellerForItem(snap, "subj", subj, "carrots"); name != "" || id != "" {
		t.Errorf("a co-present seller sold down to 0 must drop out via qty>0, got %q/%q", name, id)
	}
}

// TestObservedSupplierBuyRate_ScopedToListedSellers pins the LLM-295 seller-level
// scoping (code_review): the observed buy anchor is drawn ONLY from the sellers the
// walk-to list names (RestockVendor.VendorID) plus the co-present seller, never a
// non-rendered co-worker at the same structure. Elizabeth (listed) has sold milk at
// 2; Milo (a co-worker at the same farm that findItemVendors deduped away) at 6. The
// rate must read 2 (Elizabeth only), not 4 (the structure-wide average) — the case a
// structure-keyed scan would have gotten wrong. Direct over the helper, so it isn't
// circular with the invariant that shares it.
func TestObservedSupplierBuyRate_ScopedToListedSellers(t *testing.T) {
	published := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	elizSales := sim.NewRingBuffer[sim.PriceObservation](8)
	elizSales.Push(sim.PriceObservation{BuyerID: "x", Amount: 2, Qty: 1, Consumers: 1, At: published.Add(-1 * time.Hour)})
	miloSales := sim.NewRingBuffer[sim.PriceObservation](8)
	miloSales.Push(sim.PriceObservation{BuyerID: "x", Amount: 6, Qty: 1, Consumers: 1, At: published.Add(-1 * time.Hour)})
	snap := &sim.Snapshot{
		PublishedAt: published,
		PriceBook: map[sim.PriceBookKey]*sim.RingBuffer[sim.PriceObservation]{
			{SellerID: "elizabeth", Item: "milk"}: elizSales,
			{SellerID: "milo", Item: "milk"}:      miloSales,
		},
	}
	// Only Elizabeth is listed as the walk-to representative; Milo is the co-worker.
	if rate, ok := observedSupplierBuyRate([]RestockVendor{{StructureID: "ellis_farm", VendorID: "elizabeth"}}, "", snap, "milk", restockSalesWindow); !ok || rate != 2 {
		t.Fatalf("listed-seller-only rate = %d (ok=%v), want 2 — Milo's 6 must not average in", rate, ok)
	}
	// Both listed → the true aggregate (2+6 over 2 units = 4); proves the scoping is
	// what excludes Milo above, not an unrelated filter.
	if rate, ok := observedSupplierBuyRate([]RestockVendor{{VendorID: "elizabeth"}, {VendorID: "milo"}}, "", snap, "milk", restockSalesWindow); !ok || rate != 4 {
		t.Fatalf("both-sellers rate = %d (ok=%v), want 4", rate, ok)
	}
	// The co-present seller counts even when not in the vendor list.
	if rate, ok := observedSupplierBuyRate(nil, "milo", snap, "milk", restockSalesWindow); !ok || rate != 6 {
		t.Fatalf("co-present rate = %d (ok=%v), want 6", rate, ok)
	}
	// No listed or co-present seller → seed fallback (ok=false).
	if _, ok := observedSupplierBuyRate(nil, "", snap, "milk", restockSalesWindow); ok {
		t.Fatalf("empty suppliers should yield ok=false")
	}
}
