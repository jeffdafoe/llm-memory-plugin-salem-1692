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
		if vds, _ := findItemVendors(snap, "hannah", buyer, "milk"); len(vds) != 0 {
			t.Errorf("wholesale vendor should be dropped for a non-distributor, got %+v", vds)
		}
	})
	t.Run("wholesaler_kept_for_distributor", func(t *testing.T) {
		snap, buyer := wholesaleGateSnap("josiah", "general_store", true)
		vds, _ := findItemVendors(snap, "josiah", buyer, "milk")
		if len(vds) != 1 || vds[0].StructureID != "ellis_farm" {
			t.Errorf("wholesale vendor should be kept for the distributor, got %+v", vds)
		}
	})
	t.Run("non_wholesale_vendor_unaffected", func(t *testing.T) {
		snap, buyer := wholesaleGateSnap("hannah", "the_inn", false)
		// Untag Ellis Farm: a plain (non-wholesale) milk vendor stays visible to
		// everyone. It still produces milk, so the LLM-252 supplier gate keeps it.
		snap.VillageObjects["ellis_farm"].Tags = nil
		if vds, _ := findItemVendors(snap, "hannah", buyer, "milk"); len(vds) != 1 {
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

// TestWholesaleGate_Transformer (LLM-477): a buyer stationed at a wholesaler-tagged
// structure perceives a wholesale source for the inputs its OWN recipes require, and
// for nothing else. This is the perception half of the tier that lets the mill buy its
// wheat from the farm instead of retail from the shop it sells its flour to.
//
// The snapshot deliberately gives the buyer an explicit `buy` row for a good that feeds
// none of its recipes (water), because that is precisely where the two candidate
// predicates disagree: EffectiveBuyEntries would grant it, ProductionInputKinds does
// not. Water staying hidden is the regression pin for that decision — an operator's
// larder row must never become a wholesale exemption (the live Ellis Farm `buy: sage`
// shape).
func TestWholesaleGate_Transformer(t *testing.T) {
	transformerSnap := func() (*sim.Snapshot, *sim.ActorSnapshot) {
		snap, buyer := wholesaleGateSnap("joseph", "mill", false)
		// The buyer's own workplace is wholesale-tagged — condition 1 of the tier.
		snap.VillageObjects["mill"] = &sim.VillageObject{
			ID: "mill", OwnerActorID: "joseph", Tags: []string{sim.TagWholesaler},
		}
		// It transforms milk into cheese, and separately keeps a larder buy row for
		// water that feeds no recipe of its own.
		buyer.RestockPolicy = &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: "cheese", Source: sim.RestockSourceProduce, Max: 15},
			{Item: "water", Source: sim.RestockSourceBuy, Max: 10},
		}}
		snap.Recipes = map[sim.ItemKind]*sim.ItemRecipe{
			"cheese": {OutputItem: "cheese", OutputQty: 1, Inputs: []sim.RecipeInput{{Item: "milk", Qty: 3}}},
		}
		// Ellis produces BOTH goods, so the LLM-252 supplier gate keeps her for each
		// and the wholesale tier is the only thing that can differentiate them.
		snap.Actors["ellis"].Inventory["water"] = 20
		snap.Actors["ellis"].RestockPolicy = &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: "milk", Source: sim.RestockSourceProduce, Max: 40},
			{Item: "water", Source: sim.RestockSourceProduce, Max: 20},
		}}
		snap.ItemKinds["water"] = &sim.ItemKindDef{
			Name: "water", DisplayLabel: "water", Category: sim.ItemCategoryDrink,
			Satisfies: []sim.ItemSatisfaction{{Attribute: "thirst", Immediate: 4}},
		}
		return snap, buyer
	}

	t.Run("production_input_visible_at_the_wholesale_source", func(t *testing.T) {
		snap, buyer := transformerSnap()
		vds, _ := findItemVendors(snap, "joseph", buyer, "milk")
		if len(vds) != 1 || vds[0].StructureID != "ellis_farm" {
			t.Errorf("a transformer must see the wholesale source for its own recipe input, got %+v", vds)
		}
	})

	t.Run("non_input_still_hidden_despite_explicit_buy_row", func(t *testing.T) {
		snap, buyer := transformerSnap()
		if vds, _ := findItemVendors(snap, "joseph", buyer, "water"); len(vds) != 0 {
			t.Errorf("an explicit buy row for a non-input must NOT open the wholesale source, got %+v", vds)
		}
	})

	t.Run("raw_producer_at_a_wholesaler_sees_nothing", func(t *testing.T) {
		// A wholesaler that transforms nothing derives an empty allowance — the
		// farms-eat-each-other pin at the perception layer.
		snap, buyer := transformerSnap()
		buyer.RestockPolicy = producePolicy("carrots", 30)
		snap.Recipes = map[sim.ItemKind]*sim.ItemRecipe{
			"carrots": {OutputItem: "carrots", OutputQty: 1},
		}
		if vds, _ := findItemVendors(snap, "joseph", buyer, "milk"); len(vds) != 0 {
			t.Errorf("one wholesale producer must not see another as a source, got %+v", vds)
		}
	})

	t.Run("co_present_peer_arm_follows_the_same_grant", func(t *testing.T) {
		// The peer cue and the vendor scan must agree, or the mill gets cued to buy
		// from a huddled farmer the dispatch gate would then refuse (the LLM-289 bug
		// shape). Milk eases thirst here, so the peer offer is otherwise eligible.
		snap, buyer := transformerSnap()
		hid, h := huddleWith("joseph", "ellis")
		snap.Huddles = map[sim.HuddleID]*sim.Huddle{hid: h}
		buyer.CurrentHuddleID = hid
		buyer.Needs = map[sim.NeedKey]int{"thirst": sim.DefaultThirstRedThreshold}
		buyer.Acquaintances = map[string]sim.Acquaintance{"Ellis Ward": {}}
		buyer.Inventory = map[sim.ItemKind]int{}

		offers := gatherCoPresentPeerOffers(snap, "joseph", buyer, "thirst")
		var sawMilk, sawWater bool
		for _, o := range offers {
			switch o.itemKind {
			case "milk":
				sawMilk = true
			case "water":
				sawWater = true
			}
		}
		if !sawMilk {
			t.Error("the huddled wholesale peer's milk (a production input) should be offered to a transformer")
		}
		if sawWater {
			t.Error("the huddled wholesale peer's water (not an input) must stay gated")
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
