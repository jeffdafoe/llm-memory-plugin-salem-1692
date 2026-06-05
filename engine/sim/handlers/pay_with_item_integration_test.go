package handlers_test

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/handlers"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// pay_with_item_integration_test.go — Phase 3 PR S4 step 9.
// End-to-end exercises that walk the full pay-with-item lifecycle
// through the production interfaces: handlers register, world spins,
// real Commands fire, subscribers stamp warrants, sweep expires.
//
// Scope: pin the surfaces a future regression would break — the
// handler→Command→subscriber wiring and the sweep's in-line behavior
// when AcceptPay arrives past TTL. Gate-level failure-mode coverage is
// in pay_with_item_commands_test.go; this file's tests are whole-flow
// smoke checks.

// integrationEvents accumulates every pay-with-item event emitted so
// tests can assert ordering and content. Local to handlers_test
// because the equivalent helper in sim_test is in a different package.
type integrationEvents struct {
	Offer    []sim.PayOfferReceived
	Counter  []sim.PayCountered
	Resolved []sim.PayWithItemResolved
}

func captureIntegrationEvents(t *testing.T, w *sim.World) *integrationEvents {
	t.Helper()
	out := &integrationEvents{}
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Subscribe(sim.SubscriberFunc(func(_ *sim.World, evt sim.Event) {
			switch e := evt.(type) {
			case *sim.PayOfferReceived:
				out.Offer = append(out.Offer, *e)
			case *sim.PayCountered:
				out.Counter = append(out.Counter, *e)
			case *sim.PayWithItemResolved:
				out.Resolved = append(out.Resolved, *e)
			}
		}))
		return nil, nil
	}}); err != nil {
		t.Fatalf("captureIntegrationEvents: %v", err)
	}
	return out
}

// buildIntegrationWorld stands up a fixture suitable for end-to-end
// flows: huddle "h1" + scene "sc1" + ItemKinds + the pay-with-item
// subscribers registered.
func buildIntegrationWorld(t *testing.T) (*sim.World, func()) {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.ItemKinds.Seed(mem.SeedItemKinds())
	now := time.Now().UTC()
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"alice": {
			ID: "alice", DisplayName: "Alice", Kind: sim.KindNPCShared,
			State: sim.StateIdle, StateEnteredAt: now,
			Coins: 50, CurrentHuddleID: "h1",
			RecentActions:    sim.NewRingBuffer[sim.Action](4),
			RecentStateTrans: sim.NewRingBuffer[sim.StateTransition](4),
		},
		"bob": {
			ID: "bob", DisplayName: "Bob", Kind: sim.KindNPCShared,
			State: sim.StateIdle, StateEnteredAt: now,
			Inventory:        map[sim.ItemKind]int{"stew": 5},
			CurrentHuddleID:  "h1",
			RecentActions:    sim.NewRingBuffer[sim.Action](4),
			RecentStateTrans: sim.NewRingBuffer[sim.StateTransition](4),
		},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Huddles["h1"] = &sim.Huddle{
			ID: "h1", StartedAt: now,
			Members: map[sim.ActorID]struct{}{"alice": {}, "bob": {}},
		}
		world.Scenes["sc1"] = &sim.Scene{
			ID: "sc1", OriginAt: now, Bound: sim.NewUnboundedBound(),
			Huddles: map[sim.HuddleID]struct{}{"h1": {}},
		}
		handlers.RegisterPayWithItemHandlers(world)
		return nil, nil
	}}); err != nil {
		cancel()
		<-done
		t.Fatalf("setup: %v", err)
	}
	return w, func() { cancel(); <-done }
}

// TestIntegration_SlowPathAcceptedHappyPath — the canonical end-to-end:
//  1. Alice calls pay_with_item (slow path → Pending).
//  2. PayOfferReceived subscriber stamps PayOfferWarrantReason on Bob.
//  3. Bob calls accept_pay.
//  4. At accept, coins move (4 alice→bob) AND the physical takeaway is
//     delivered immediately (ZBBS-HOME-398): bob's stew drops 5→4 and alice
//     receives 1. The Order is minted then flipped straight to Delivered (no
//     deferred deliver_order beat for physical goods — that's lodging-only).
//  5. PayWithItemResolved subscriber stamps PayResolvedWarrantReason
//     on Alice. OrderCreated + OrderDelivered both fire under the same root.
func TestIntegration_SlowPathAcceptedHappyPath(t *testing.T) {
	w, stop := buildIntegrationWorld(t)
	defer stop()

	aliceCmd, err := handlers.HandlePayWithItem(handlers.HandlerInput{
		ActorID:   "alice",
		AttemptID: "tk-1",
		Args: handlers.PayWithItemArgs{
			Seller: "Bob", Item: "stew", Qty: 1, Amount: 4,
			ConsumeNow: false,
		},
	})
	if err != nil {
		t.Fatalf("HandlePayWithItem: %v", err)
	}
	res, err := w.Send(aliceCmd)
	if err != nil {
		t.Fatalf("PayWithItem send: %v", err)
	}
	result := res.(sim.PayWithItemResult)
	if result.State != sim.PayLedgerStatePending || result.FastPath {
		t.Fatalf("slow-path expected pending; got %+v", result)
	}

	bobAccept, err := handlers.HandleAcceptPay(handlers.HandlerInput{
		ActorID:   "bob",
		AttemptID: "tk-2",
		Args:      handlers.AcceptPayArgs{LedgerID: uint64(result.LedgerID)},
	})
	if err != nil {
		t.Fatalf("HandleAcceptPay: %v", err)
	}
	if _, err := w.Send(bobAccept); err != nil {
		t.Fatalf("AcceptPay send: %v", err)
	}

	snap := w.Published()
	if got := snap.Actors["alice"].Coins; got != 46 {
		t.Errorf("alice.Coins = %d, want 46", got)
	}
	if got := snap.Actors["bob"].Coins; got != 4 {
		t.Errorf("bob.Coins = %d, want 4", got)
	}
	// ZBBS-HOME-398: physical takeaway is delivered to alice at accept —
	// bob's stew drops 5→4 and alice receives 1.
	var aliceStew, bobStew int
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		aliceStew = world.Actors["alice"].Inventory["stew"]
		bobStew = world.Actors["bob"].Inventory["stew"]
		return nil, nil
	}}); err != nil {
		t.Fatalf("read inventory: %v", err)
	}
	if aliceStew != 1 {
		t.Errorf("alice stew = %d, want 1 (delivered at accept)", aliceStew)
	}
	if bobStew != 4 {
		t.Errorf("bob stew = %d, want 4 (delivered at accept)", bobStew)
	}
	// The Order was minted then immediately delivered (never left Ready).
	var foundOrder *sim.Order
	for _, o := range snap.Orders {
		if o != nil && o.SellerID == "bob" && o.BuyerID == "alice" {
			foundOrder = o
			break
		}
	}
	if foundOrder == nil {
		t.Fatalf("no Order recorded at accept; snapshot.Orders = %+v", snap.Orders)
	}
	if foundOrder.State != sim.OrderStateDelivered {
		t.Errorf("order State = %q, want delivered (immediate handover)", foundOrder.State)
	}
	if foundOrder.Item != "stew" || foundOrder.Qty != 1 {
		t.Errorf("Order item/qty = %s/%d, want stew/1", foundOrder.Item, foundOrder.Qty)
	}

	// Alice (buyer) carries a PayResolvedWarrant from the accept.
	warrants := readWarrants(t, w, "alice")
	if meta, ok := firstByKind(warrants, sim.WarrantKindPayResolved); !ok {
		t.Errorf("alice missing PayResolved warrant; warrants = %+v", warrants)
	} else if meta.Reason.(sim.PayResolvedWarrantReason).TerminalState != sim.PayTerminalStateAccepted {
		t.Errorf("PayResolved.TerminalState = %q, want accepted",
			meta.Reason.(sim.PayResolvedWarrantReason).TerminalState)
	}
}

// TestIntegration_CounterChain — Alice offers, Bob counters, Alice
// responds via in_response_to with a higher amount, Bob accepts. The
// chain depth, ParentID linkage, and dual-direction warrant flow all
// land correctly.
func TestIntegration_CounterChain(t *testing.T) {
	w, stop := buildIntegrationWorld(t)
	defer stop()

	// Round 1: alice offers 4 coins.
	offer, err := handlers.HandlePayWithItem(handlers.HandlerInput{
		ActorID: "alice", AttemptID: "tk-1",
		Args: handlers.PayWithItemArgs{Seller: "Bob", Item: "stew", Qty: 1, Amount: 4, ConsumeNow: false},
	})
	if err != nil {
		t.Fatalf("HandlePayWithItem: %v", err)
	}
	r1, err := w.Send(offer)
	if err != nil {
		t.Fatalf("Round 1 PayWithItem: %v", err)
	}
	parentID := r1.(sim.PayWithItemResult).LedgerID

	// Round 2: bob counters at 6.
	counter, err := handlers.HandleCounterPay(handlers.HandlerInput{
		ActorID: "bob", AttemptID: "tk-2",
		Args: handlers.CounterPayArgs{LedgerID: uint64(parentID), Amount: 6, Message: "how about six"},
	})
	if err != nil {
		t.Fatalf("HandleCounterPay: %v", err)
	}
	if _, err := w.Send(counter); err != nil {
		t.Fatalf("Round 2 CounterPay: %v", err)
	}

	// Round 3: alice responds with 6, in_response_to=parentID.
	response, err := handlers.HandlePayWithItem(handlers.HandlerInput{
		ActorID: "alice", AttemptID: "tk-3",
		Args: handlers.PayWithItemArgs{
			Seller: "Bob", Item: "stew", Qty: 1, Amount: 6,
			ConsumeNow: false, InResponseTo: uint64(parentID),
		},
	})
	if err != nil {
		t.Fatalf("HandlePayWithItem response: %v", err)
	}
	r3, err := w.Send(response)
	if err != nil {
		t.Fatalf("Round 3 PayWithItem: %v", err)
	}
	childID := r3.(sim.PayWithItemResult).LedgerID

	// Verify chain shape.
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return []sim.PayLedgerEntry{*world.PayLedger[parentID], *world.PayLedger[childID]}, nil
	}})
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	entries := res.([]sim.PayLedgerEntry)
	if entries[0].State != sim.PayLedgerStateCountered {
		t.Errorf("parent.State = %q, want countered", entries[0].State)
	}
	if entries[0].CounterAmount != 6 {
		t.Errorf("parent.CounterAmount = %d, want 6", entries[0].CounterAmount)
	}
	if entries[1].ParentID != parentID || entries[1].Depth != 1 {
		t.Errorf("child ParentID/Depth = %d/%d, want %d/1", entries[1].ParentID, entries[1].Depth, parentID)
	}

	// Round 4: bob accepts the child.
	accept, err := handlers.HandleAcceptPay(handlers.HandlerInput{
		ActorID: "bob", AttemptID: "tk-4",
		Args: handlers.AcceptPayArgs{LedgerID: uint64(childID)},
	})
	if err != nil {
		t.Fatalf("HandleAcceptPay: %v", err)
	}
	if _, err := w.Send(accept); err != nil {
		t.Fatalf("Round 4 AcceptPay: %v", err)
	}
	snap := w.Published()
	if got := snap.Actors["alice"].Coins; got != 44 {
		t.Errorf("alice.Coins = %d, want 44 (50 - 6)", got)
	}
	if got := snap.Actors["bob"].Coins; got != 6 {
		t.Errorf("bob.Coins = %d, want 6", got)
	}
}

// TestIntegration_ExpiredViaSweep — alice offers, time advances, sweep
// flips the entry to Expired, alice gets a PayResolvedWarrant with
// TerminalState=Expired. Bob's PayOfferWarrant from the original
// stamp is left as-is (warrants are ephemeral; reactor consumes them
// on next tick).
func TestIntegration_ExpiredViaSweep(t *testing.T) {
	w, stop := buildIntegrationWorld(t)
	defer stop()

	// Squeeze the TTL so we can drive expiry without waiting.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Settings.PayLedgerTTL = 1 * time.Microsecond
		return nil, nil
	}}); err != nil {
		t.Fatalf("set TTL: %v", err)
	}

	offer, _ := handlers.HandlePayWithItem(handlers.HandlerInput{
		ActorID: "alice", AttemptID: "tk-1",
		Args: handlers.PayWithItemArgs{Seller: "Bob", Item: "stew", Qty: 1, Amount: 4, ConsumeNow: false},
	})
	res, _ := w.Send(offer)
	ledgerID := res.(sim.PayWithItemResult).LedgerID

	// Sweep with a "now" comfortably past the entry's ExpiresAt.
	sweepAt := time.Now().UTC().Add(time.Hour)
	if _, err := w.Send(sim.EvaluatePayLedgerSweep(sweepAt)); err != nil {
		t.Fatalf("EvaluatePayLedgerSweep: %v", err)
	}
	state, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return world.PayLedger[ledgerID].State, nil
	}})
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	if state != sim.PayLedgerStateExpired {
		t.Errorf("state = %q, want expired", state)
	}
	// Alice got the PayResolved{Expired} warrant.
	if meta, ok := firstByKind(readWarrants(t, w, "alice"), sim.WarrantKindPayResolved); !ok {
		t.Error("alice missing PayResolved warrant after sweep")
	} else if meta.Reason.(sim.PayResolvedWarrantReason).TerminalState != sim.PayTerminalStateExpired {
		t.Errorf("TerminalState = %q, want expired",
			meta.Reason.(sim.PayResolvedWarrantReason).TerminalState)
	}
}

// TestIntegration_AcceptPastTTL — accept_pay arriving in-band after
// the entry's ExpiresAt drives the Expired flip directly (gate 5 of
// the 10-gate revalidation matrix). No sweep needed; AcceptPay
// flips, emits, and the buyer warrant carries TerminalState=Expired.
func TestIntegration_AcceptPastTTL(t *testing.T) {
	w, stop := buildIntegrationWorld(t)
	defer stop()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Settings.PayLedgerTTL = 1 * time.Microsecond
		return nil, nil
	}}); err != nil {
		t.Fatalf("set TTL: %v", err)
	}

	offer, _ := handlers.HandlePayWithItem(handlers.HandlerInput{
		ActorID: "alice", AttemptID: "tk-1",
		Args: handlers.PayWithItemArgs{Seller: "Bob", Item: "stew", Qty: 1, Amount: 4, ConsumeNow: false},
	})
	res, _ := w.Send(offer)
	ledgerID := res.(sim.PayWithItemResult).LedgerID

	// Advance time well past TTL by calling sim.AcceptPay directly
	// with an explicit `at` instead of HandleAcceptPay (which captures
	// time.Now at handler-call time, not at Send-time — and with a
	// 1µs TTL the handler captures a `now` that may not yet be past
	// ExpiresAt). Direct invocation avoids the timing race.
	sweepAt := time.Now().UTC().Add(time.Hour)
	if _, err := w.Send(sim.AcceptPay("bob", ledgerID, sweepAt)); err != nil {
		t.Fatalf("AcceptPay: %v", err)
	}
	state, _ := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return world.PayLedger[ledgerID].State, nil
	}})
	if state != sim.PayLedgerStateExpired {
		t.Errorf("state = %q, want expired (accept past TTL)", state)
	}
	// No coin movement.
	snap := w.Published()
	if got := snap.Actors["alice"].Coins; got != 50 {
		t.Errorf("alice.Coins moved on expired accept: %d", got)
	}
}

// TestIntegration_QuoteFastPath — bob posts a quote (via the existing
// scene_quote handler); alice's pay_with_item references quote_id and
// hits the fast path; coins/items move immediately, PayWithItemResolved
// fires with FastPath=true, and PayOfferReceived is NOT emitted
// (architecture § 4 — quote fast-path skips the pending state).
func TestIntegration_QuoteFastPath(t *testing.T) {
	w, stop := buildIntegrationWorld(t)
	defer stop()
	events := captureIntegrationEvents(t, w)

	// Bob posts a quote for 1 stew at 4 coins.
	quoteCmd, err := handlers.HandleSceneQuote(handlers.HandlerInput{
		ActorID: "bob", AttemptID: "tk-1",
		Args: handlers.SceneQuoteArgs{ItemKind: "stew", Qty: 1, Amount: 4, ConsumeNow: false},
	})
	if err != nil {
		t.Fatalf("HandleSceneQuote: %v", err)
	}
	qRes, err := w.Send(quoteCmd)
	if err != nil {
		t.Fatalf("SceneQuote send: %v", err)
	}
	quoteID := qRes.(sim.SceneQuoteCreateResult).QuoteID

	// Alice takes the quote via fast path.
	payCmd, err := handlers.HandlePayWithItem(handlers.HandlerInput{
		ActorID: "alice", AttemptID: "tk-2",
		Args: handlers.PayWithItemArgs{
			Seller: "Bob", Item: "stew", Qty: 1, Amount: 4,
			ConsumeNow: false, QuoteID: uint64(quoteID),
		},
	})
	if err != nil {
		t.Fatalf("HandlePayWithItem fast-path: %v", err)
	}
	res, err := w.Send(payCmd)
	if err != nil {
		t.Fatalf("PayWithItem fast-path send: %v", err)
	}
	result := res.(sim.PayWithItemResult)
	if !result.FastPath || result.State != sim.PayLedgerStateAccepted {
		t.Errorf("fast-path result = %+v", result)
	}
	if len(events.Offer) != 0 {
		t.Errorf("PayOfferReceived emitted on fast path: %v", events.Offer)
	}
	if len(events.Resolved) != 1 || events.Resolved[0].TerminalState != sim.PayTerminalStateAccepted {
		t.Errorf("PayWithItemResolved = %+v", events.Resolved)
	}
	// Coins + items transferred.
	snap := w.Published()
	if got := snap.Actors["alice"].Coins; got != 46 {
		t.Errorf("alice.Coins = %d, want 46", got)
	}
}
