package perception

import (
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// farm_wholesale_test.go — LLM-223. The perception half of the farm wholesale
// tier: eachVendorOffer (the shared scan behind BOTH the restock buy directory,
// findItemVendors, AND the satiation/consumption cues, findVendorConsumables)
// drops farm-tagged vendors for every buyer except the distributor. Exercised
// through both public finders so the shared-scan claim is proven on both surfaces.

// farmWholesaleSnap builds a snapshot with a milk-selling farm vendor (Ellis at
// Ellis Farm, tagged TagFarm) and the named buyer. buyerWork sets the buyer's work
// anchor; distributorTagged marks that anchor as the distributor store, making the
// buyer the distributor. Buyer holds ample coin and no price/shut memory, so the
// farm filter is the ONLY thing that can drop the vendor.
func farmWholesaleSnap(buyerID sim.ActorID, buyerWork sim.StructureID, distributorTagged bool) (*sim.Snapshot, *sim.ActorSnapshot) {
	buyer := &sim.ActorSnapshot{
		DisplayName:     "Buyer",
		Coins:           100,
		WorkStructureID: buyerWork,
		Inventory:       map[sim.ItemKind]int{"milk": 0},
		RestockPolicy:   buyPolicy("milk", 12),
	}
	ellis := &sim.ActorSnapshot{
		DisplayName:     "Ellis Ward",
		WorkStructureID: "ellis_farm",
		Inventory:       map[sim.ItemKind]int{"milk": 40},
	}
	objects := map[sim.VillageObjectID]*sim.VillageObject{
		"ellis_farm": {ID: "ellis_farm", OwnerActorID: "ellis", Tags: []string{sim.TagFarm}},
	}
	if distributorTagged {
		objects[sim.VillageObjectID(buyerWork)] = &sim.VillageObject{
			ID: sim.VillageObjectID(buyerWork), OwnerActorID: buyerID, Tags: []string{sim.TagDistributor},
		}
	}
	snap := &sim.Snapshot{
		Actors: map[sim.ActorID]*sim.ActorSnapshot{buyerID: buyer, "ellis": ellis},
		Structures: map[sim.StructureID]*sim.Structure{
			"ellis_farm": {ID: "ellis_farm", DisplayName: "Ellis Farm"},
		},
		VillageObjects: objects,
		ItemKinds: map[sim.ItemKind]*sim.ItemKindDef{
			"milk": {Name: "milk", DisplayLabel: "milk", Category: sim.ItemCategoryDrink,
				Satisfies: []sim.ItemSatisfaction{{Attribute: "thirst", Immediate: 6}}},
		},
		RestockReorderPct: 25,
	}
	return snap, buyer
}

func TestFarmWholesale_RestockDirectory(t *testing.T) {
	t.Run("farm_dropped_for_non_distributor", func(t *testing.T) {
		snap, buyer := farmWholesaleSnap("hannah", "the_inn", false)
		if vds := findItemVendors(snap, "hannah", buyer, "milk"); len(vds) != 0 {
			t.Errorf("farm vendor should be dropped for a non-distributor, got %+v", vds)
		}
	})
	t.Run("farm_kept_for_distributor", func(t *testing.T) {
		snap, buyer := farmWholesaleSnap("josiah", "general_store", true)
		vds := findItemVendors(snap, "josiah", buyer, "milk")
		if len(vds) != 1 || vds[0].StructureID != "ellis_farm" {
			t.Errorf("farm vendor should be kept for the distributor, got %+v", vds)
		}
	})
	t.Run("non_farm_vendor_unaffected", func(t *testing.T) {
		snap, buyer := farmWholesaleSnap("hannah", "the_inn", false)
		// Untag Ellis Farm: a plain (non-farm) milk vendor stays visible to everyone.
		snap.VillageObjects["ellis_farm"].Tags = nil
		if vds := findItemVendors(snap, "hannah", buyer, "milk"); len(vds) != 1 {
			t.Errorf("a non-farm vendor must stay visible to a non-distributor, got %+v", vds)
		}
	})
}

func TestFarmWholesale_SatiationCue(t *testing.T) {
	// The satiation/consumption finder rides the SAME eachVendorOffer scan, so the
	// farm is dropped there too — a thirsty non-distributor is never pointed at a
	// farm for a drink the PayWithItem backstop would then refuse.
	snap, _ := farmWholesaleSnap("hannah", "the_inn", false)
	if cs := findVendorConsumables(snap, "hannah", "thirst", ""); len(cs) != 0 {
		t.Errorf("farm consumable vendor should be dropped for a non-distributor, got %+v", cs)
	}
	snapD, _ := farmWholesaleSnap("josiah", "general_store", true)
	if cs := findVendorConsumables(snapD, "josiah", "thirst", ""); len(cs) != 1 {
		t.Errorf("farm consumable vendor should be visible to the distributor, got %+v", cs)
	}
}
