package sim_test

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// events_pay_with_item_test.go — PR S4 event family coverage. The
// three events ship without any subscribers in this step; later steps
// add the pay-offer warrant subscriber and the resolution warrant
// subscriber. These tests pin the event shapes and the EventBase
// identity flow.

// Compile-time checks: pointer receivers on EventBase mean only
// *ConcreteEvent satisfies sim.Event. A `var _ sim.Event = sim.PayOfferReceived{}`
// line would fail to build — that's the property the assignments below
// lock in.
var (
	_ sim.Event = &sim.PayOfferReceived{}
	_ sim.Event = &sim.PayCountered{}
	_ sim.Event = &sim.PayWithItemResolved{}
)

// TestPayOfferReceived_EventIdentityFlow — emitting a pay-offer event
// through World.emit stamps a non-zero EventID and (for a fresh-origin
// emit) makes the event its own RootEventID. Same identity contract
// as every other sim.Event; this test pins the integration without
// driving a full Command Fn.
func TestPayOfferReceived_EventIdentityFlow(t *testing.T) {
	repo, _ := mem.NewRepository()
	w := sim.NewWorld(repo)

	var received sim.Event
	w.Subscribe(sim.SubscriberFunc(func(_ *sim.World, evt sim.Event) {
		received = evt
	}))

	now := time.Now().UTC()
	sim.EmitForTest(w, &sim.PayOfferReceived{
		LedgerID:       7,
		BuyerID:        "alice",
		SellerID:       "bob",
		ItemKind:       "stew",
		QtyPerConsumer: 1,
		Amount:         4,
		ConsumeNow:     true,
		SceneID:        "sc1",
		HuddleID:       "h1",
		ExpiresAt:      now.Add(3 * time.Minute),
		At:             now,
	})

	if received == nil {
		t.Fatal("subscriber did not receive PayOfferReceived")
	}
	if received.EventID() == 0 {
		t.Error("EventID == 0 — emit should have stamped a non-zero id")
	}
	if received.RootEventID() != received.EventID() {
		t.Errorf("fresh-origin RootEventID = %d, want EventID %d (event is its own root)",
			received.RootEventID(), received.EventID())
	}

	evt, ok := received.(*sim.PayOfferReceived)
	if !ok {
		t.Fatalf("subscriber got %T, want *PayOfferReceived", received)
	}
	if evt.LedgerID != 7 || evt.BuyerID != "alice" || evt.SellerID != "bob" ||
		evt.ItemKind != "stew" || evt.QtyPerConsumer != 1 || evt.Amount != 4 ||
		!evt.ConsumeNow || evt.SceneID != "sc1" || evt.HuddleID != "h1" {
		t.Errorf("payload fields not preserved: %+v", evt)
	}
}

// TestPayCountered_EventIdentityFlow — same identity contract for
// the PayCountered event family. Carries OriginalAmount + CounterAmount
// + Message for the buyer's resolution-warrant subscriber to render
// the counter-proposal terms.
func TestPayCountered_EventIdentityFlow(t *testing.T) {
	repo, _ := mem.NewRepository()
	w := sim.NewWorld(repo)

	var received sim.Event
	w.Subscribe(sim.SubscriberFunc(func(_ *sim.World, evt sim.Event) {
		received = evt
	}))

	now := time.Now().UTC()
	sim.EmitForTest(w, &sim.PayCountered{
		ParentID:       12,
		BuyerID:        "alice",
		SellerID:       "bob",
		ItemKind:       "stew",
		QtyPerConsumer: 1,
		ConsumeNow:     true,
		OriginalAmount: 4,
		CounterAmount:  6,
		Message:        "stew runs more than that",
		SceneID:        "sc1",
		HuddleID:       "h1",
		At:             now,
	})

	if received == nil {
		t.Fatal("subscriber did not receive PayCountered")
	}
	if received.EventID() == 0 {
		t.Error("EventID == 0 — emit should have stamped a non-zero id")
	}
	evt, ok := received.(*sim.PayCountered)
	if !ok {
		t.Fatalf("subscriber got %T, want *PayCountered", received)
	}
	if evt.ParentID != 12 || evt.OriginalAmount != 4 || evt.CounterAmount != 6 ||
		evt.Message != "stew runs more than that" {
		t.Errorf("counter-specific fields not preserved: %+v", evt)
	}
}

// TestPayWithItemResolved_EventIdentityFlow — same identity contract;
// also verifies the TerminalState field is type-checked at the
// PayTerminalState boundary (any of the 8 terminal values).
func TestPayWithItemResolved_EventIdentityFlow(t *testing.T) {
	repo, _ := mem.NewRepository()
	w := sim.NewWorld(repo)

	var received sim.Event
	w.Subscribe(sim.SubscriberFunc(func(_ *sim.World, evt sim.Event) {
		received = evt
	}))

	now := time.Now().UTC()
	sim.EmitForTest(w, &sim.PayWithItemResolved{
		LedgerID:       42,
		BuyerID:        "alice",
		SellerID:       "bob",
		ItemKind:       "stew",
		QtyPerConsumer: 1,
		ConsumeNow:     true,
		Amount:         4,
		TerminalState:  sim.PayTerminalStateAccepted,
		SceneID:        "sc1",
		HuddleID:       "h1",
		At:             now,
	})

	if received == nil {
		t.Fatal("subscriber did not receive PayWithItemResolved")
	}
	if received.EventID() == 0 {
		t.Error("EventID == 0 — emit should have stamped a non-zero id")
	}
	evt, ok := received.(*sim.PayWithItemResolved)
	if !ok {
		t.Fatalf("subscriber got %T, want *PayWithItemResolved", received)
	}
	if evt.LedgerID != 42 || evt.TerminalState != sim.PayTerminalStateAccepted {
		t.Errorf("resolved-specific fields not preserved: %+v", evt)
	}
}

// TestPayTerminalState_StringValuesMatchPayLedgerState — the two enums
// have identical underlying strings (PayTerminalState is PayLedgerState
// minus Pending). This catches the "added a new state to one enum but
// not the other" drift mode without needing a runtime conversion
// helper.
func TestPayTerminalState_StringValuesMatchPayLedgerState(t *testing.T) {
	pairs := []struct {
		state    sim.PayLedgerState
		terminal sim.PayTerminalState
	}{
		{sim.PayLedgerStateAccepted, sim.PayTerminalStateAccepted},
		{sim.PayLedgerStateDeclined, sim.PayTerminalStateDeclined},
		{sim.PayLedgerStateCountered, sim.PayTerminalStateCountered},
		{sim.PayLedgerStateWithdrawnByBuyer, sim.PayTerminalStateWithdrawnByBuyer},
		{sim.PayLedgerStateExpired, sim.PayTerminalStateExpired},
		{sim.PayLedgerStateFailedInsufficientFunds, sim.PayTerminalStateFailedInsufficientFunds},
		{sim.PayLedgerStateFailedInsufficientStock, sim.PayTerminalStateFailedInsufficientStock},
		{sim.PayLedgerStateFailedInsufficientGoods, sim.PayTerminalStateFailedInsufficientGoods},
		{sim.PayLedgerStateFailedUnavailable, sim.PayTerminalStateFailedUnavailable},
	}
	for _, p := range pairs {
		if string(p.state) != string(p.terminal) {
			t.Errorf("PayLedgerState %q != PayTerminalState %q (underlying strings must match)",
				p.state, p.terminal)
		}
	}
}
