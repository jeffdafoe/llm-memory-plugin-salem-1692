package sim_test

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// pay_with_item_reverse_buy_own_ware_test.go — LLM-293. The general reverse-pay
// guard: a seller must not fire the buyer verb for one of its OWN wares when the
// named seller can't actually supply it. Live: Hannah (innkeeper, porridge
// producer) answered a customer's "I would like to buy a bowl" with
// pay_with_item{seller:<customer>, item:"porridge"}, offering to buy her own
// porridge back — a phantom reverse offer. Generalizes the LLM-291 wholesale arm
// (IsOwnProduce) to any vendor via RestockPolicy.Manages, gated by whether the
// named counterparty can fill the offer (counterpartyCanSupply) so a legitimate
// restock stays allowed.
//
// Helpers (buildPayWithItemWorld, pwiActor, mustSend) live in
// pay_with_item_commands_test.go — same sim_test package. Seeded catalog goods:
// stew (john's produced ware), wheat (john's bought-to-resell ware, produced by
// Elizabeth), bread (unrelated).
func ownWareReverseBuyWorld(t *testing.T) (*sim.World, func(), time.Time) {
	t.Helper()
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "john", displayName: "John Ellis", kind: sim.KindNPCStateful, huddleID: "h1", coins: 30, inventory: map[sim.ItemKind]int{"stew": 5}},
		{id: "lewis", displayName: "Lewis Walker", kind: sim.KindNPCShared, huddleID: "h1", coins: 20, inventory: map[sim.ItemKind]int{}},
		{id: "elizabeth", displayName: "Elizabeth Ellis", kind: sim.KindNPCShared, huddleID: "h1", coins: 20, inventory: map[sim.ItemKind]int{}},
		{id: "patience", displayName: "Patience Walker", kind: sim.KindNPCShared, huddleID: "h1", coins: 20, inventory: map[sim.ItemKind]int{"stew": 2}},
		{id: "josiah", displayName: "Josiah Thorne", kind: sim.KindNPCStateful, huddleID: "h1", coins: 30, inventory: map[sim.ItemKind]int{}},
	})
	mustSend(t, w, func(world *sim.World) {
		if world.VillageObjects == nil {
			world.VillageObjects = map[sim.VillageObjectID]*sim.VillageObject{}
		}
		// John the tavernkeeper PRODUCES stew and RESELLS wheat — both are his own
		// wares (RestockPolicy.Manages), neither at a wholesaler workplace.
		world.Actors["john"].RestockPolicy = &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: "stew", Source: sim.RestockSourceProduce, Max: 10},
			{Item: "wheat", Source: sim.RestockSourceBuy, Max: 10},
		}}
		// Elizabeth is a first-hand wheat supplier.
		world.Actors["elizabeth"].RestockPolicy = &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: "wheat", Source: sim.RestockSourceProduce, Max: 30},
		}}
		// Josiah is the village distributor (standing supplier of everything).
		world.Actors["josiah"].WorkStructureID = "general_store"
		world.VillageObjects["general_store"] = &sim.VillageObject{
			ID: "general_store", OwnerActorID: "josiah", Tags: []string{sim.TagDistributor},
		}
	})
	return w, stop, time.Now().UTC()
}

func TestPayWithItem_OwnWareReverseBuyGate(t *testing.T) {
	t.Run("seller_buying_own_ware_from_customer_rejected", func(t *testing.T) {
		w, stop, at := ownWareReverseBuyWorld(t)
		defer stop()
		// John (produces stew) names his customer Lewis — who holds no stew and
		// doesn't make it — as seller and tries to "buy" stew back. Phantom flip.
		_, err := w.Send(sim.PayWithItem("john", "Lewis Walker", "stew", 1, 2, false, nil, nil, 0, 0, "", at))
		if err == nil {
			t.Fatalf("a seller buying its own ware back from a non-supplier should be rejected, got nil err")
		}
		for _, want := range []string{"you sell", "accept_pay"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("reject steer %q should contain %q (steer to the sell path)", err.Error(), want)
			}
		}
	})

	t.Run("reseller_restock_from_producer_allowed", func(t *testing.T) {
		w, stop, at := ownWareReverseBuyWorld(t)
		defer stop()
		// John RESELLS wheat (Manages via a buy entry), so it is one of his wares —
		// but buying it FROM Elizabeth, who produces it, is a legitimate restock:
		// the supplier can fill the offer, so the guard must not fire.
		res, err := w.Send(sim.PayWithItem("john", "Elizabeth Ellis", "wheat", 1, 2, false, nil, nil, 0, 0, "", at))
		if err != nil {
			t.Fatalf("restocking a resale good from its producer should pass the guard: %v", err)
		}
		if res.(sim.PayWithItemResult).State != sim.PayLedgerStatePending {
			t.Errorf("state = %q, want pending (slow-path mint)", res.(sim.PayWithItemResult).State)
		}
	})

	t.Run("buying_own_ware_from_a_holder_allowed", func(t *testing.T) {
		w, stop, at := ownWareReverseBuyWorld(t)
		defer stop()
		// Patience holds stew. John "buying" stew from her is an odd direction, but
		// the offer is FILLABLE (she has the goods) — not the phantom deadlock the
		// guard targets — so it is allowed rather than hard-blocked.
		res, err := w.Send(sim.PayWithItem("john", "Patience Walker", "stew", 1, 2, false, nil, nil, 0, 0, "", at))
		if err != nil {
			t.Fatalf("buying an own ware from a co-present holder should pass the guard: %v", err)
		}
		if res.(sim.PayWithItemResult).State != sim.PayLedgerStatePending {
			t.Errorf("state = %q, want pending", res.(sim.PayWithItemResult).State)
		}
	})

	t.Run("buying_own_ware_from_distributor_allowed", func(t *testing.T) {
		w, stop, at := ownWareReverseBuyWorld(t)
		defer stop()
		// The distributor is the standing supplier of everything, so buying an own
		// ware from Josiah is legitimate restock — the guard defers to him.
		res, err := w.Send(sim.PayWithItem("john", "Josiah Thorne", "stew", 1, 2, false, nil, nil, 0, 0, "", at))
		if err != nil {
			t.Fatalf("buying an own ware from the distributor should pass the guard: %v", err)
		}
		if res.(sim.PayWithItemResult).State != sim.PayLedgerStatePending {
			t.Errorf("state = %q, want pending", res.(sim.PayWithItemResult).State)
		}
	})

	t.Run("buying_an_unrelated_good_allowed", func(t *testing.T) {
		w, stop, at := ownWareReverseBuyWorld(t)
		defer stop()
		// Bread is NOT one of John's wares (Manages is false), so even buying it
		// from a non-supplier is an ordinary buy — the guard is item-scoped.
		res, err := w.Send(sim.PayWithItem("john", "Lewis Walker", "bread", 1, 2, false, nil, nil, 0, 0, "", at))
		if err != nil {
			t.Fatalf("buying a good the caller does not deal in should not be gated: %v", err)
		}
		if res.(sim.PayWithItemResult).State != sim.PayLedgerStatePending {
			t.Errorf("state = %q, want pending", res.(sim.PayWithItemResult).State)
		}
	})
}
