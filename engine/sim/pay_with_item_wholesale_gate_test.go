package sim_test

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// pay_with_item_wholesale_gate_test.go — LLM-223 wholesale tier, generalized to
// the wholesaler tag in LLM-252. The engine backstop: a non-distributor buying
// from a seller stationed at a wholesaler-tagged structure is rejected at dispatch
// and steered to the distributor; the distributor buys wholesale freely; a seller
// NOT at a wholesaler is never gated. The traded good is arbitrary (bread —
// portable, so no eat-here clamp perturbs the slow-path mint); the gate keys on the
// SELLER's workplace tag, not the item.
//
// Helpers (buildPayWithItemWorld, pwiActor, mustSend) live in
// pay_with_item_commands_test.go — same sim_test package.

// tagWholesaleWorld seeds a wholesale keeper (Moses at James Farm, tagged
// farm+wholesaler as a live farm is, holding bread) plus two prospective buyers —
// a distributor (Josiah, keeper of the distributor-tagged General Store) and a
// plain innkeeper (Hannah) — all co-present in one huddle. It sets each actor's
// work anchor and the village-object tags, which pwiActor doesn't carry, via a
// follow-up command on the world goroutine.
func tagWholesaleWorld(t *testing.T) (*sim.World, func(), time.Time) {
	t.Helper()
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "moses", displayName: "Moses James", kind: sim.KindNPCShared, huddleID: "h1", coins: 30, inventory: map[sim.ItemKind]int{"bread": 40}},
		{id: "josiah", displayName: "Josiah Thorne", kind: sim.KindNPCStateful, huddleID: "h1", coins: 30, inventory: map[sim.ItemKind]int{}},
		{id: "hannah", displayName: "Hannah Boggs", kind: sim.KindNPCShared, huddleID: "h1", coins: 30, inventory: map[sim.ItemKind]int{}},
	})
	mustSend(t, w, func(world *sim.World) {
		if world.VillageObjects == nil {
			world.VillageObjects = map[sim.VillageObjectID]*sim.VillageObject{}
		}
		world.Actors["moses"].WorkStructureID = "james_farm"
		world.Actors["josiah"].WorkStructureID = "general_store"
		world.Actors["hannah"].WorkStructureID = "the_inn"
		world.VillageObjects["james_farm"] = &sim.VillageObject{
			ID: "james_farm", OwnerActorID: "moses", Tags: []string{sim.TagFarm, sim.TagWholesaler},
		}
		world.VillageObjects["general_store"] = &sim.VillageObject{
			ID: "general_store", OwnerActorID: "josiah", Tags: []string{sim.TagDistributor},
		}
	})
	return w, stop, time.Now().UTC()
}

// makeMillTransformer turns Hannah into the LLM-477 transformer case: her workplace
// becomes a wholesaler-tagged mill and she produces ale from bread, so bread is a
// required input of her own recipes. Everything else about tagWholesaleWorld stands —
// Moses still sells from the wholesale-tagged James Farm.
func makeMillTransformer(t *testing.T, w *sim.World) {
	t.Helper()
	mustSend(t, w, func(world *sim.World) {
		world.Recipes = map[sim.ItemKind]*sim.ItemRecipe{
			"ale": {OutputItem: "ale", OutputQty: 2, Inputs: []sim.RecipeInput{{Item: "bread", Qty: 1}}},
		}
		world.Actors["hannah"].WorkStructureID = "mill"
		world.Actors["hannah"].RestockPolicy = &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: "ale", Source: sim.RestockSourceProduce, Max: 20},
		}}
		world.VillageObjects["mill"] = &sim.VillageObject{
			ID: "mill", OwnerActorID: "hannah", Tags: []string{sim.TagWholesaler},
		}
	})
}

func TestPayWithItem_WholesaleGate(t *testing.T) {
	t.Run("non_distributor_buying_from_wholesaler_rejected", func(t *testing.T) {
		w, stop, at := tagWholesaleWorld(t)
		defer stop()
		_, err := w.Send(sim.PayWithItem("hannah", "Moses James", "bread", 1, 4, false, nil, nil, 0, 0, "", at))
		// LLM-292 copy constraint: the steer speaks in-world ("whose shop supplies
		// the village"), never the mechanic-role word "distributor".
		if err == nil || !strings.Contains(err.Error(), "wholesale goods are sold only to") {
			t.Fatalf("wholesale buy by a non-distributor err = %v, want wholesale-gate steer", err)
		}
		if strings.Contains(strings.ToLower(err.Error()), "distributor") {
			t.Errorf("steer %q carries the mechanic-role term \"distributor\" (LLM-292 copy constraint)", err.Error())
		}
		// The steer names the distributor so the model knows where to shop instead.
		if !strings.Contains(err.Error(), "Josiah Thorne") {
			t.Errorf("steer %q should name the distributor (Josiah Thorne)", err.Error())
		}
	})

	t.Run("distributor_buying_from_wholesaler_allowed", func(t *testing.T) {
		w, stop, at := tagWholesaleWorld(t)
		defer stop()
		res, err := w.Send(sim.PayWithItem("josiah", "Moses James", "bread", 1, 4, false, nil, nil, 0, 0, "", at))
		if err != nil {
			t.Fatalf("the distributor buying from a wholesaler should pass the gate: %v", err)
		}
		if res.(sim.PayWithItemResult).State != sim.PayLedgerStatePending {
			t.Errorf("state = %q, want pending (mints normally)", res.(sim.PayWithItemResult).State)
		}
	})

	// LLM-477: the transformer tier. A buyer stationed at a wholesaler-tagged
	// structure buys the inputs its OWN recipes require straight from a wholesale
	// source — the mill's wheat — but nothing else. The mem test repo seeds only
	// ale/bread/water, so the live wheat→flour shape is modelled here as
	// bread→ale: Joseph produces ale at the (wholesaler-tagged) mill and bread is
	// its recipe input, standing in for the miller buying wheat from the farm.
	t.Run("transformer_buying_its_recipe_input_from_wholesaler_allowed", func(t *testing.T) {
		w, stop, at := tagWholesaleWorld(t)
		defer stop()
		makeMillTransformer(t, w)
		res, err := w.Send(sim.PayWithItem("hannah", "Moses James", "bread", 1, 4, false, nil, nil, 0, 0, "", at))
		if err != nil {
			t.Fatalf("a transformer buying its own production input wholesale should pass the gate: %v", err)
		}
		if res.(sim.PayWithItemResult).State != sim.PayLedgerStatePending {
			t.Errorf("state = %q, want pending (mints normally)", res.(sim.PayWithItemResult).State)
		}
	})

	t.Run("transformer_buying_a_non_input_from_wholesaler_rejected", func(t *testing.T) {
		w, stop, at := tagWholesaleWorld(t)
		defer stop()
		makeMillTransformer(t, w)
		// Water feeds none of the transformer's recipes, so the grant does not reach
		// it: the tier stays scoped to production inputs, not a blanket exemption for
		// anyone who happens to be wholesaler-tagged.
		_, err := w.Send(sim.PayWithItem("hannah", "Moses James", "water", 1, 4, false, nil, nil, 0, 0, "", at))
		if err == nil || !strings.Contains(err.Error(), "wholesale goods are sold only to") {
			t.Fatalf("a transformer buying a NON-input wholesale err = %v, want wholesale-gate steer", err)
		}
	})

	t.Run("non_wholesaler_transformer_still_gated", func(t *testing.T) {
		w, stop, at := tagWholesaleWorld(t)
		defer stop()
		// Same recipe + produce policy, but the buyer's workplace is NOT
		// wholesaler-tagged — condition 1 fails, so an ordinary keeper who transforms
		// goods stays routed through the distributor. This is what keeps the tier from
		// widening to every producer in the village.
		mustSend(t, w, func(world *sim.World) {
			world.Recipes = map[sim.ItemKind]*sim.ItemRecipe{
				"ale": {OutputItem: "ale", OutputQty: 2, Inputs: []sim.RecipeInput{{Item: "bread", Qty: 1}}},
			}
			world.Actors["hannah"].RestockPolicy = &sim.RestockPolicy{Restock: []sim.RestockEntry{
				{Item: "ale", Source: sim.RestockSourceProduce, Max: 20},
			}}
		})
		_, err := w.Send(sim.PayWithItem("hannah", "Moses James", "bread", 1, 4, false, nil, nil, 0, 0, "", at))
		if err == nil || !strings.Contains(err.Error(), "wholesale goods are sold only to") {
			t.Fatalf("a non-wholesaler transformer err = %v, want wholesale-gate steer", err)
		}
	})

	t.Run("non_distributor_buying_from_non_wholesaler_allowed", func(t *testing.T) {
		w, stop, at := tagWholesaleWorld(t)
		defer stop()
		// Move Moses off the wholesale structure: with no wholesaler-tagged workplace
		// his sale to a non-distributor is an ordinary offer, untouched by the gate.
		mustSend(t, w, func(world *sim.World) {
			world.Actors["moses"].WorkStructureID = "the_market"
		})
		res, err := w.Send(sim.PayWithItem("hannah", "Moses James", "bread", 1, 4, false, nil, nil, 0, 0, "", at))
		if err != nil {
			t.Fatalf("buying from a non-wholesaler seller should not be gated: %v", err)
		}
		if res.(sim.PayWithItemResult).State != sim.PayLedgerStatePending {
			t.Errorf("state = %q, want pending (slow-path mint)", res.(sim.PayWithItemResult).State)
		}
	})
}
