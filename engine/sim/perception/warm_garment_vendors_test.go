package perception

import (
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// warm_garment_vendors_test.go — LLM-410 unit coverage for findWarmGarmentVendors,
// the structural finder behind the cold "buy a coat" nudge. Exercises the buyer-
// unscoped (empty buyerID) scan directly: only non-PC sellers holding qty>0 of a
// CapabilityWarms good at a resolvable workplace surface, one entry per structure,
// deterministically ordered — and nothing surfaces when no warm stock exists (the
// vendor-gating).

// warmVendorCatalog: coat + cloak carry warms; bread does not.
func warmVendorCatalog() map[sim.ItemKind]*sim.ItemKindDef {
	return map[sim.ItemKind]*sim.ItemKindDef{
		"coat":  {Name: "coat", DisplayLabel: "Coat", Category: "clothing", Capabilities: []string{sim.CapabilityWarms}},
		"cloak": {Name: "cloak", DisplayLabel: "Cloak", Category: "clothing", Capabilities: []string{sim.CapabilityWarms}},
		"bread": {Name: "bread", DisplayLabel: "Bread", Category: sim.ItemCategoryFood},
	}
}

func TestFindWarmGarmentVendors(t *testing.T) {
	// Josiah sells coats at the General Store; a clothier sells cloaks at the Market;
	// a baker stocks only bread; a PC holds a coat; a keeper holds a spent (0) coat.
	// Only the two warm-garment SELLERS at resolvable workplaces surface, sorted by
	// structure label.
	snap := &sim.Snapshot{
		ItemKinds: warmVendorCatalog(),
		Structures: map[sim.StructureID]*sim.Structure{
			"general_store": plainStructure("general_store", "General Store"),
			"market":        plainStructure("market", "Market Stall"),
			"bakery":        plainStructure("bakery", "Bakery"),
		},
		Actors: map[sim.ActorID]*sim.ActorSnapshot{
			"josiah":   {Kind: sim.KindNPCStateful, DisplayName: "Josiah", WorkStructureID: "general_store", Inventory: map[sim.ItemKind]int{"coat": 2, "bread": 3}},
			"clothier": {Kind: sim.KindNPCShared, DisplayName: "Clothier", WorkStructureID: "market", Inventory: map[sim.ItemKind]int{"cloak": 1}},
			"baker":    {Kind: sim.KindNPCStateful, DisplayName: "Baker", WorkStructureID: "bakery", Inventory: map[sim.ItemKind]int{"bread": 5}},
			"pc":       {Kind: sim.KindPC, DisplayName: "Player", WorkStructureID: "general_store", Inventory: map[sim.ItemKind]int{"coat": 1}},
			"spent":    {Kind: sim.KindNPCStateful, DisplayName: "Spent", WorkStructureID: "market", Inventory: map[sim.ItemKind]int{"coat": 0}},
		},
	}

	got := findWarmGarmentVendors(snap)
	if len(got) != 2 {
		t.Fatalf("got %d vendors, want 2 (General Store + Market): %+v", len(got), got)
	}
	// Sorted by structure label: "General Store" < "Market Stall".
	if got[0].StructureID != "general_store" || got[1].StructureID != "market" {
		t.Errorf("order = [%s, %s], want [general_store, market]", got[0].StructureID, got[1].StructureID)
	}
	for _, v := range got {
		if v.StructureID == "bakery" {
			t.Errorf("bread-only bakery surfaced as a warm-garment vendor")
		}
	}
}

// TestFindWarmGarmentVendors_MultiVendorOneStructure: two warm-garment sellers at one
// structure collapse to a single entry (lowest VendorID the representative), so the
// nudge names the workplace once.
func TestFindWarmGarmentVendors_MultiVendorOneStructure(t *testing.T) {
	snap := &sim.Snapshot{
		ItemKinds: warmVendorCatalog(),
		Structures: map[sim.StructureID]*sim.Structure{
			"general_store": plainStructure("general_store", "General Store"),
		},
		Actors: map[sim.ActorID]*sim.ActorSnapshot{
			"anna": {Kind: sim.KindNPCStateful, WorkStructureID: "general_store", Inventory: map[sim.ItemKind]int{"coat": 1}},
			"bram": {Kind: sim.KindNPCStateful, WorkStructureID: "general_store", Inventory: map[sim.ItemKind]int{"cloak": 1}},
		},
	}
	got := findWarmGarmentVendors(snap)
	if len(got) != 1 {
		t.Fatalf("got %d, want 1 entry for the shared structure: %+v", len(got), got)
	}
	if got[0].VendorID != "anna" {
		t.Errorf("representative = %s, want anna (lowest VendorID)", got[0].VendorID)
	}
}

// TestFindWarmGarmentVendors_None: no warm stock anywhere → nil, so the nudge is
// vendor-gated off (no dangling steer before supply exists).
func TestFindWarmGarmentVendors_None(t *testing.T) {
	snap := &sim.Snapshot{
		ItemKinds:  warmVendorCatalog(),
		Structures: map[sim.StructureID]*sim.Structure{"bakery": plainStructure("bakery", "Bakery")},
		Actors: map[sim.ActorID]*sim.ActorSnapshot{
			"baker": {Kind: sim.KindNPCStateful, WorkStructureID: "bakery", Inventory: map[sim.ItemKind]int{"bread": 5}},
		},
	}
	if got := findWarmGarmentVendors(snap); got != nil {
		t.Errorf("got %+v, want nil (no warm stock → vendor-gated off)", got)
	}
}
