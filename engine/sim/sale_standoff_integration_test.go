package sim_test

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// sale_standoff_integration_test.go — LLM-525. The capture unit tests
// (sale_standoff_capture_test.go) drive handleSaleStandoffOnResolved directly, so
// they cannot see whether the subscriber is wired, whether the real DeclinePay path
// emits the event the subscriber expects, or whether the ledger state and the event
// timestamps are populated the way the threshold count assumes. This drives the whole
// path: register the subscriber, seed two pending offers in one conversation, decline
// both through w.Send(sim.DeclinePay(...)), and read the memory back off the published
// snapshot — the same snapshot perception builds from (code_review LLM-525).

// standoffWorld builds the two-actor conversation, gives the seller a workplace (the
// memory is keyed by it), and registers the subscriber. Both mutations go through the
// world goroutine, as the production wiring does.
func standoffWorld(t *testing.T) (*sim.World, func()) {
	t.Helper()
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCStateful, huddleID: "h1", coins: 50},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCStateful, huddleID: "h1", inventory: map[sim.ItemKind]int{"stew": 4}},
	})
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["bob"].WorkStructureID = "tavern"
		sim.RegisterSaleStandoffSubscriber(world)
		return nil, nil
	}}); err != nil {
		stop()
		t.Fatalf("wire standoff world: %v", err)
	}
	return w, stop
}

// declineOffer seeds a pending offer and declines it through the real command.
func declineOffer(t *testing.T, w *sim.World, id sim.LedgerID, at time.Time) {
	t.Helper()
	seedLedgerEntry(t, w, sim.PayLedgerEntry{
		ID: id, BuyerID: "alice", SellerID: "bob",
		ItemKind: "stew", Qty: 1, Amount: 4,
		State: sim.PayLedgerStatePending, ExpiresAt: at.Add(3 * time.Minute),
		SceneID: "sc1", HuddleID: "h1",
	})
	if _, err := w.Send(sim.DeclinePay("bob", id, "not today", at)); err != nil {
		t.Fatalf("DeclinePay(%d): %v", id, err)
	}
}

func standoffMemory(t *testing.T, w *sim.World) (time.Time, bool) {
	t.Helper()
	buyer := w.Published().Actors["alice"]
	if buyer == nil {
		t.Fatal("buyer missing from the published snapshot")
	}
	return buyer.Observed.At(sim.ObservedStateKey{
		StructureID: "tavern", ItemKind: "stew", Condition: sim.ObservedSaleStandoff,
	})
}

// The threshold holds end-to-end: one real decline leaves the shop in the directory,
// the second records the standoff — against the SELLER's workplace, stamped with the
// decline's own time, and visible on the published snapshot perception reads.
func TestSaleStandoffIntegration_SecondDeclineRecordsOnSnapshot(t *testing.T) {
	w, stop := standoffWorld(t)
	defer stop()
	first := time.Now().UTC()

	declineOffer(t, w, 1, first)
	if _, ok := standoffMemory(t, w); ok {
		t.Fatal("one decline is ordinary haggling — no standoff memory should exist yet")
	}

	second := first.Add(9 * time.Minute)
	declineOffer(t, w, 2, second)
	at, ok := standoffMemory(t, w)
	if !ok {
		t.Fatal("the second decline in the conversation must record the standoff on the published snapshot")
	}
	if !at.Equal(second) {
		t.Errorf("memory stamped at %v, want the second decline's time %v", at, second)
	}
}

// The subscriber must be wired to fire at all — without RegisterSaleStandoffSubscriber
// the identical sequence records nothing, which is what a missing registration in the
// engine startup path would look like.
func TestSaleStandoffIntegration_UnregisteredWorldRecordsNothing(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCStateful, huddleID: "h1", coins: 50},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCStateful, huddleID: "h1", inventory: map[sim.ItemKind]int{"stew": 4}},
	})
	defer stop()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["bob"].WorkStructureID = "tavern"
		return nil, nil
	}}); err != nil {
		t.Fatalf("set workplace: %v", err)
	}
	at := time.Now().UTC()
	declineOffer(t, w, 1, at)
	declineOffer(t, w, 2, at.Add(time.Minute))

	if _, ok := standoffMemory(t, w); ok {
		t.Fatal("no subscriber registered — nothing should have been recorded")
	}
}

// A real accepted buy clears a standing memory: they dealt after all, so the shop
// belongs back in the directory. Exercises the clear arm through the same command path
// rather than a hand-built event.
func TestSaleStandoffIntegration_AcceptedBuyClearsMemory(t *testing.T) {
	w, stop := standoffWorld(t)
	defer stop()
	at := time.Now().UTC()
	declineOffer(t, w, 1, at)
	declineOffer(t, w, 2, at.Add(time.Minute))
	if _, ok := standoffMemory(t, w); !ok {
		t.Fatal("precondition: two declines should have recorded the standoff")
	}

	settled := at.Add(2 * time.Minute)
	seedLedgerEntry(t, w, sim.PayLedgerEntry{
		ID: 3, BuyerID: "alice", SellerID: "bob",
		ItemKind: "stew", Qty: 1, Amount: 4,
		State: sim.PayLedgerStatePending, ExpiresAt: settled.Add(3 * time.Minute),
		SceneID: "sc1", HuddleID: "h1",
	})
	if _, err := w.Send(sim.AcceptPay("bob", 3, settled)); err != nil {
		t.Fatalf("AcceptPay: %v", err)
	}

	if _, ok := standoffMemory(t, w); ok {
		t.Fatal("an accepted buy must clear the standoff memory")
	}
}
