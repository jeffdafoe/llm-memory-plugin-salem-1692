package perception

import (
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// wholesale_gate_test.go — LLM-223, generalized to the wholesaler tag in LLM-252.
// The perception half of the wholesale tier: eachVendorOffer (the shared scan
// behind BOTH the restock buy directory, findItemVendors, AND the
// satiation/consumption cues, findVendorConsumables) drops wholesaler-tagged
// vendors for every buyer except the distributor. Exercised through both public
// finders so the shared-scan claim is proven on both surfaces.

// wholesaleGateSnap builds a snapshot with a milk-selling wholesale vendor (Ellis
// at Ellis Farm, tagged TagWholesaler — a farm carries both farm+wholesaler in the
// live data, but only the wholesaler tag gates selling) and the named buyer.
// buyerWork sets the buyer's work anchor; distributorTagged marks that anchor as
// the distributor store, making the buyer the distributor. The vendor produces
// milk (so the LLM-252 restock-supplier gate keeps it — isolating the wholesale
// gate as the only thing under test), and the buyer holds ample coin with no
// price/shut memory, so the wholesale filter is the ONLY thing that can drop it.
func wholesaleGateSnap(buyerID sim.ActorID, buyerWork sim.StructureID, distributorTagged bool) (*sim.Snapshot, *sim.ActorSnapshot) {
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
		RestockPolicy:   producePolicy("milk", 40),
	}
	objects := map[sim.VillageObjectID]*sim.VillageObject{
		// A live farm carries both tags; only wholesaler gates selling (farm now
		// scopes just the upkeep tax).
		"ellis_farm": {ID: "ellis_farm", OwnerActorID: "ellis", Tags: []string{sim.TagFarm, sim.TagWholesaler}},
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

func TestWholesaleGate_RestockDirectory(t *testing.T) {
	t.Run("wholesaler_dropped_for_non_distributor", func(t *testing.T) {
		snap, buyer := wholesaleGateSnap("hannah", "the_inn", false)
		if vds := findItemVendors(snap, "hannah", buyer, "milk"); len(vds) != 0 {
			t.Errorf("wholesale vendor should be dropped for a non-distributor, got %+v", vds)
		}
	})
	t.Run("wholesaler_kept_for_distributor", func(t *testing.T) {
		snap, buyer := wholesaleGateSnap("josiah", "general_store", true)
		vds := findItemVendors(snap, "josiah", buyer, "milk")
		if len(vds) != 1 || vds[0].StructureID != "ellis_farm" {
			t.Errorf("wholesale vendor should be kept for the distributor, got %+v", vds)
		}
	})
	t.Run("non_wholesale_vendor_unaffected", func(t *testing.T) {
		snap, buyer := wholesaleGateSnap("hannah", "the_inn", false)
		// Untag Ellis Farm: a plain (non-wholesale) milk vendor stays visible to
		// everyone. It still produces milk, so the LLM-252 supplier gate keeps it.
		snap.VillageObjects["ellis_farm"].Tags = nil
		if vds := findItemVendors(snap, "hannah", buyer, "milk"); len(vds) != 1 {
			t.Errorf("a non-wholesale vendor must stay visible to a non-distributor, got %+v", vds)
		}
	})
}

// TestWholesaleGate_CoPresentPeerCue (LLM-289): the CO-PRESENT PEER arm of the
// satiation cue must apply the same wholesale gate as the vendor scan. The
// dispatch gate keys on the SELLER's work anchor wherever the seller stands, so
// a huddled wholesaler-farmer carrying his own produce is a guaranteed
// pay_with_item rejection for a non-distributor — the cue must not advertise
// it (live hud-843da92a: 40 of 57 turns burned on cued wholesale rejections).
// The distributor keeps the offer.
func TestWholesaleGate_CoPresentPeerCue(t *testing.T) {
	peerCueSnap := func(buyerID sim.ActorID, buyerWork sim.StructureID, distributorTagged bool) (*sim.Snapshot, *sim.ActorSnapshot) {
		snap, buyer := wholesaleGateSnap(buyerID, buyerWork, distributorTagged)
		// Put the buyer in a huddle with Ellis, thirsty enough to feel it. The
		// buyer holds coin (means-to-pay) and no milk (degenerate-buy), so the
		// wholesale gate is the only thing that can drop Ellis's offer.
		hid, h := huddleWith(buyerID, "ellis")
		snap.Huddles = map[sim.HuddleID]*sim.Huddle{hid: h}
		buyer.CurrentHuddleID = hid
		buyer.Needs = map[sim.NeedKey]int{"thirst": sim.DefaultThirstRedThreshold}
		buyer.Acquaintances = map[string]sim.Acquaintance{"Ellis Ward": {}}
		return snap, buyer
	}
	t.Run("wholesaler_peer_dropped_for_non_distributor", func(t *testing.T) {
		snap, buyer := peerCueSnap("hannah", "the_inn", false)
		if offers := gatherCoPresentPeerOffers(snap, "hannah", buyer, "thirst"); len(offers) != 0 {
			t.Errorf("huddled wholesaler peer should be dropped for a non-distributor, got %+v", offers)
		}
	})
	t.Run("wholesaler_peer_kept_for_distributor", func(t *testing.T) {
		snap, buyer := peerCueSnap("josiah", "general_store", true)
		offers := gatherCoPresentPeerOffers(snap, "josiah", buyer, "thirst")
		if len(offers) != 1 || offers[0].PeerLabel != "Ellis Ward" {
			t.Errorf("huddled wholesaler peer should stay visible to the distributor, got %+v", offers)
		}
	})
	t.Run("non_wholesale_peer_unaffected", func(t *testing.T) {
		snap, buyer := peerCueSnap("hannah", "the_inn", false)
		snap.VillageObjects["ellis_farm"].Tags = nil
		if offers := gatherCoPresentPeerOffers(snap, "hannah", buyer, "thirst"); len(offers) != 1 {
			t.Errorf("a non-wholesale huddled peer must stay offered, got %+v", offers)
		}
	})
	t.Run("workless_peer_unaffected", func(t *testing.T) {
		// An empty work anchor is never a wholesaler (SellerAtWholesaler's
		// explicit "" guard) — a workless peer carrying a satisfier keeps
		// its offer.
		snap, buyer := peerCueSnap("hannah", "the_inn", false)
		snap.Actors["ellis"].WorkStructureID = ""
		if offers := gatherCoPresentPeerOffers(snap, "hannah", buyer, "thirst"); len(offers) != 1 {
			t.Errorf("a workless huddled peer must stay offered, got %+v", offers)
		}
	})
}

func TestWholesaleGate_SatiationCue(t *testing.T) {
	// The satiation/consumption finder rides the SAME eachVendorOffer scan, so the
	// wholesale source is dropped there too — a thirsty non-distributor is never
	// pointed at a wholesaler for a drink the PayWithItem backstop would then refuse.
	snap, _ := wholesaleGateSnap("hannah", "the_inn", false)
	if cs := findVendorConsumables(snap, "hannah", "thirst", ""); len(cs) != 0 {
		t.Errorf("wholesale consumable vendor should be dropped for a non-distributor, got %+v", cs)
	}
	snapD, _ := wholesaleGateSnap("josiah", "general_store", true)
	if cs := findVendorConsumables(snapD, "josiah", "thirst", ""); len(cs) != 1 {
		t.Errorf("wholesale consumable vendor should be visible to the distributor, got %+v", cs)
	}
}
