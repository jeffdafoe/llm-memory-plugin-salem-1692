package sim_test

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// pay_with_item_producer_reverse_buy_test.go — LLM-291. A wholesale producer
// must not fire the buyer verb for its OWN produce. Live hud-9b23…: Moses (James
// Farm, wholesaler), pressed to answer a customer who wanted his wheat, named the
// CUSTOMER as "seller" and staked a pay_with_item to "buy" his own wheat back — a
// phantom reverse-direction offer the customer could never fill. The dispatch gate
// keys on sim.IsOwnProduce (caller works at a wholesaler AND the item is one of its
// produce rows), the same predicate the Consume guard and eat-cue filter on, so it
// is item-scoped: a farmhand buying UNRELATED goods is untouched.
//
// Helpers (buildPayWithItemWorld, pwiActor, mustSend) live in
// pay_with_item_commands_test.go — same sim_test package.

// producerReverseBuyWorld seeds a wholesale producer (Moses at James Farm, tagged
// farm+wholesaler, produces wheat) huddled with a would-be customer (Silence), a
// plain innkeeper (Hannah, holding bread), and the village distributor (Josiah,
// keeper of the distributor-tagged General Store). All co-present in one huddle so
// each can transact; Josiah's keepership lets the reject steer name him. pwiActor
// carries no work anchor / RestockPolicy / object tags, so a follow-up command on
// the world goroutine sets them.
func producerReverseBuyWorld(t *testing.T) (*sim.World, func(), time.Time) {
	t.Helper()
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "moses", displayName: "Moses James", kind: sim.KindNPCShared, huddleID: "h1", coins: 30, inventory: map[sim.ItemKind]int{"wheat": 30}},
		{id: "silence", displayName: "Silence Walker", kind: sim.KindNPCShared, huddleID: "h1", coins: 22, inventory: map[sim.ItemKind]int{}},
		{id: "hannah", displayName: "Hannah Boggs", kind: sim.KindNPCShared, huddleID: "h1", coins: 30, inventory: map[sim.ItemKind]int{"bread": 10}},
		{id: "josiah", displayName: "Josiah Thorne", kind: sim.KindNPCStateful, huddleID: "h1", coins: 30, inventory: map[sim.ItemKind]int{}},
	})
	mustSend(t, w, func(world *sim.World) {
		if world.VillageObjects == nil {
			world.VillageObjects = map[sim.VillageObjectID]*sim.VillageObject{}
		}
		world.Actors["moses"].WorkStructureID = "james_farm"
		// Moses grows wheat — the produce row IsOwnProduce keys on.
		world.Actors["moses"].RestockPolicy = &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: "wheat", Source: sim.RestockSourceProduce, Max: 30},
		}}
		world.Actors["josiah"].WorkStructureID = "general_store"
		world.VillageObjects["james_farm"] = &sim.VillageObject{
			ID: "james_farm", OwnerActorID: "moses", Tags: []string{sim.TagFarm, sim.TagWholesaler},
		}
		world.VillageObjects["general_store"] = &sim.VillageObject{
			ID: "general_store", OwnerActorID: "josiah", Tags: []string{sim.TagDistributor},
		}
	})
	return w, stop, time.Now().UTC()
}

func TestPayWithItem_ProducerReverseBuyGate(t *testing.T) {
	t.Run("producer_buying_own_produce_back_rejected", func(t *testing.T) {
		w, stop, at := producerReverseBuyWorld(t)
		defer stop()
		// Moses names the customer as "seller" and tries to buy his own wheat back.
		_, err := w.Send(sim.PayWithItem("moses", "Silence Walker", "wheat", 5, 5, false, nil, nil, 0, 0, "trade", at))
		if err == nil {
			t.Fatalf("a wholesale producer buying its own produce back should be rejected at dispatch, got nil err")
		}
		// The steer tells the producer it doesn't buy its own goods and routes the
		// customer to the wholesale channel — naming the distributor.
		for _, want := range []string{"you produce", "wholesale", "Josiah Thorne"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("reject steer %q should contain %q", err.Error(), want)
			}
		}
	})

	t.Run("producer_buying_own_produce_from_distributor_still_rejected", func(t *testing.T) {
		w, stop, at := producerReverseBuyWorld(t)
		defer stop()
		// The prohibition is absolute — it keys on the CALLER being the producer,
		// not on who is named as seller. Even naming the distributor (the one actor
		// who could legitimately hold this good) doesn't open a buy-back path: the
		// only sanctioned flow is the distributor buying FROM the producer.
		_, err := w.Send(sim.PayWithItem("moses", "Josiah Thorne", "wheat", 5, 5, false, nil, nil, 0, 0, "", at))
		if err == nil {
			t.Fatalf("a wholesale producer buying its own produce back — even from the distributor — should be rejected, got nil err")
		}
		if !strings.Contains(err.Error(), "you produce") {
			t.Errorf("reject steer %q should carry the producer buy-back steer", err.Error())
		}
	})

	t.Run("producer_buying_unrelated_good_allowed", func(t *testing.T) {
		w, stop, at := producerReverseBuyWorld(t)
		defer stop()
		// Bread is NOT one of Moses's produce rows and Hannah is not a wholesaler,
		// so this ordinary purchase clears both the producer arm (item-scoped) and
		// the wholesale seller gate — proving a farmhand can still buy what it needs.
		res, err := w.Send(sim.PayWithItem("moses", "Hannah Boggs", "bread", 1, 4, false, nil, nil, 0, 0, "", at))
		if err != nil {
			t.Fatalf("a producer buying a good it does NOT produce should not be gated: %v", err)
		}
		if res.(sim.PayWithItemResult).State != sim.PayLedgerStatePending {
			t.Errorf("state = %q, want pending (slow-path mint)", res.(sim.PayWithItemResult).State)
		}
	})

	t.Run("non_producer_restock_of_wheat_allowed", func(t *testing.T) {
		w, stop, at := producerReverseBuyWorld(t)
		defer stop()
		// The distributor legitimately buys wheat FROM the wholesale producer: the
		// producer arm keys on the CALLER (Josiah doesn't grow wheat), and the
		// distributor is exempt from the wholesale seller gate. Sanity that the new
		// arm doesn't leak onto a genuine buyer-side purchase of the same good.
		res, err := w.Send(sim.PayWithItem("josiah", "Moses James", "wheat", 5, 5, false, nil, nil, 0, 0, "", at))
		if err != nil {
			t.Fatalf("the distributor buying wheat from the producer should pass: %v", err)
		}
		if res.(sim.PayWithItemResult).State != sim.PayLedgerStatePending {
			t.Errorf("state = %q, want pending (mints normally)", res.(sim.PayWithItemResult).State)
		}
	})
}
