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
	if strings.Contains(out, "ask") || strings.Contains(out, "price") {
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
