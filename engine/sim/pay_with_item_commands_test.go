package sim_test

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// pay_with_item_commands_test.go — Phase 3 PR S4 step 5 coverage of the
// five Command Fns: PayWithItem (slow + fast path), AcceptPay,
// DeclinePay, CounterPay, WithdrawPay.
//
// Handler-side decode + bounds tests live in handlers/pay_with_item_test.go
// (later step). Substrate-only tests (clone, sequence, restart helpers)
// live in pay_ledger_test.go and were locked in the PR S4 substrate
// commit.

// pwiActor — minimal actor seed for pay-with-item Command tests. Mirrors
// quoteTestActor + payActorSpec; bundling fields for both because S4
// gates exercise both vendor-side (stock, break, scene) and buyer-side
// (coins, huddle, move) state simultaneously.
type pwiActor struct {
	id           sim.ActorID
	displayName  string
	kind         sim.ActorKind
	huddleID     sim.HuddleID
	coins        int
	inventory    map[sim.ItemKind]int
	breakUntil   *time.Time
	moveInFlight bool
}

// buildPayWithItemWorld constructs the standard fixture used by every
// test in this file. Seeded with the mem ItemKind catalog (so "stew",
// "ale", "bread" resolve), then one huddle + one scene observing it.
// Actors with huddleID matching join that huddle; others stay outside.
func buildPayWithItemWorld(t *testing.T, huddleID sim.HuddleID, sceneID sim.SceneID, actors []pwiActor) (*sim.World, func()) {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.ItemKinds.Seed(mem.SeedItemKinds())

	now := time.Now().UTC()
	seed := make(map[sim.ActorID]*sim.Actor, len(actors))
	members := make(map[sim.ActorID]struct{}, len(actors))
	for _, s := range actors {
		a := &sim.Actor{
			ID:              s.id,
			DisplayName:     s.displayName,
			Kind:            s.kind,
			State:           sim.StateIdle,
			Coins:           s.coins,
			Inventory:       s.inventory,
			CurrentHuddleID: s.huddleID,
			BreakUntil:      s.breakUntil,
			RecentActions:   sim.NewRingBuffer[sim.Action](4),
		}
		if s.moveInFlight {
			a.MoveIntent = &sim.MoveIntent{AttemptID: sim.MovementAttemptID(1)}
		}
		seed[s.id] = a
		if s.huddleID == huddleID && huddleID != "" {
			members[s.id] = struct{}{}
		}
	}
	handles.Actors.Seed(seed)

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

	if huddleID != "" {
		if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
			world.Huddles[huddleID] = &sim.Huddle{
				ID:        huddleID,
				Members:   members,
				StartedAt: now,
			}
			world.Scenes[sceneID] = &sim.Scene{
				ID:       sceneID,
				OriginAt: now,
				Bound:    sim.NewUnboundedBound(),
				Huddles:  map[sim.HuddleID]struct{}{huddleID: {}},
			}
			sim.RebuildIndicesForTest(world)
			return nil, nil
		}}); err != nil {
			cancel()
			<-done
			t.Fatalf("seed scene+huddle: %v", err)
		}
	}
	return w, func() { cancel(); <-done }
}

// capturePayWithItemEvents subscribes to every PR S4 event family and
// returns slices that accumulate copies as events emit. Cleaner than
// three separate captures when a test needs to observe several at once.
type pwiEvents struct {
	Offer    []sim.PayOfferReceived
	Counter  []sim.PayCountered
	Resolved []sim.PayWithItemResolved
	Consumed []sim.ItemConsumed
}

func capturePayWithItemEvents(t *testing.T, w *sim.World) *pwiEvents {
	t.Helper()
	out := &pwiEvents{}
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Subscribe(sim.SubscriberFunc(func(_ *sim.World, evt sim.Event) {
			switch e := evt.(type) {
			case *sim.PayOfferReceived:
				out.Offer = append(out.Offer, *e)
			case *sim.PayCountered:
				out.Counter = append(out.Counter, *e)
			case *sim.PayWithItemResolved:
				out.Resolved = append(out.Resolved, *e)
			case *sim.ItemConsumed:
				out.Consumed = append(out.Consumed, *e)
			}
		}))
		return nil, nil
	}}); err != nil {
		t.Fatalf("capturePayWithItemEvents subscribe: %v", err)
	}
	return out
}

// readPayLedger returns a snapshot of World.PayLedger for inspection,
// taken on the world goroutine. Terminal-state entries stay in the map
// (the sweep doesn't remove them), so tests can assert on the final
// state of any ledger entry after a command.
func readPayLedger(t *testing.T, w *sim.World) map[sim.LedgerID]sim.PayLedgerEntry {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		out := make(map[sim.LedgerID]sim.PayLedgerEntry, len(world.PayLedger))
		for id, e := range world.PayLedger {
			if e == nil {
				continue
			}
			out[id] = *e
		}
		return out, nil
	}})
	if err != nil {
		t.Fatalf("readPayLedger: %v", err)
	}
	return res.(map[sim.LedgerID]sim.PayLedgerEntry)
}

// seedLedgerEntry directly inserts a PayLedgerEntry into world state for
// tests that exercise AcceptPay / DeclinePay / CounterPay / WithdrawPay
// against a pre-seeded pending entry (the alternative — calling
// PayWithItem to mint one — couples the gate test to PayWithItem's own
// validation, which obscures the failure surface).
func seedLedgerEntry(t *testing.T, w *sim.World, entry sim.PayLedgerEntry) {
	t.Helper()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		cp := entry
		if entry.ConsumerIDs != nil {
			cp.ConsumerIDs = append([]sim.ActorID(nil), entry.ConsumerIDs...)
		}
		world.PayLedger[entry.ID] = &cp
		return nil, nil
	}}); err != nil {
		t.Fatalf("seedLedgerEntry: %v", err)
	}
}

// seedQuote directly inserts a SceneQuote into World.Quotes for
// fast-path tests. Mirrors seedLedgerEntry's posture.
func seedQuote(t *testing.T, w *sim.World, quote sim.SceneQuote) {
	t.Helper()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		cp := quote
		if quote.ConsumerIDs != nil {
			cp.ConsumerIDs = append([]sim.ActorID(nil), quote.ConsumerIDs...)
		}
		world.Quotes[quote.ID] = &cp
		scene := world.Scenes[quote.SceneID]
		if scene != nil {
			scene.QuoteIDs = append(scene.QuoteIDs, quote.ID)
		}
		return nil, nil
	}}); err != nil {
		t.Fatalf("seedQuote: %v", err)
	}
}

// ============================================================
// PayWithItem — slow path
// ============================================================

func TestPayWithItem_SlowPath_HappyPath(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCShared, huddleID: "h1", coins: 10},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1", inventory: map[sim.ItemKind]int{"stew": 5}},
	})
	defer stop()

	events := capturePayWithItemEvents(t, w)
	at := time.Now().UTC()

	res, err := w.Send(sim.PayWithItem("alice", "Bob", "stew", 1, 4, false, nil, nil, 0, 0, "", at))
	if err != nil {
		t.Fatalf("PayWithItem: %v", err)
	}
	result, ok := res.(sim.PayWithItemResult)
	if !ok {
		t.Fatalf("result type = %T, want PayWithItemResult", res)
	}
	if result.State != sim.PayLedgerStatePending {
		t.Errorf("State = %q, want pending", result.State)
	}
	if result.FastPath {
		t.Error("FastPath = true on slow path call")
	}
	if result.LedgerID == 0 {
		t.Error("LedgerID = 0 (must be non-zero)")
	}

	if len(events.Offer) != 1 {
		t.Fatalf("PayOfferReceived events = %d, want 1", len(events.Offer))
	}
	if got := events.Offer[0]; got.BuyerID != "alice" || got.SellerID != "bob" ||
		got.ItemKind != "stew" || got.QtyPerConsumer != 1 || got.Amount != 4 ||
		got.HuddleID != "h1" || got.SceneID != "sc1" {
		t.Errorf("PayOfferReceived payload = %+v", got)
	}
	if len(events.Resolved) != 0 {
		t.Errorf("PayWithItemResolved emitted on slow-path mint: %v", events.Resolved)
	}

	// No coin or inventory movement on slow-path mint.
	snap := w.Published()
	if snap.Actors["alice"].Coins != 10 {
		t.Errorf("alice.Coins moved on slow-path mint: %d", snap.Actors["alice"].Coins)
	}

	// Entry is in world state with correct shape.
	ledger := readPayLedger(t, w)
	entry, ok := ledger[result.LedgerID]
	if !ok {
		t.Fatalf("ledger entry %d not in World.PayLedger", result.LedgerID)
	}
	if entry.BuyerID != "alice" || entry.SellerID != "bob" || entry.State != sim.PayLedgerStatePending {
		t.Errorf("entry = %+v", entry)
	}
	if entry.ExpiresAt.Sub(at) != sim.PayLedgerTTLDefault {
		t.Errorf("ExpiresAt - at = %v, want %v", entry.ExpiresAt.Sub(at), sim.PayLedgerTTLDefault)
	}
}

// TestPayWithItem_SlowPath_InsufficientFunds_FastFail covers the
// ZBBS-WORK-231 offer-time funds fast-fail: a buyer who can't cover the
// offer is rejected at mint with a tool error, so no pending entry is
// minted and the seller's warrant never fires (no PayOfferReceived).
// This is the optimization half — AcceptPay's gate 11 remains the
// authoritative funds check and is exercised separately.
func TestPayWithItem_SlowPath_InsufficientFunds_FastFail(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCShared, huddleID: "h1", coins: 3},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1", inventory: map[sim.ItemKind]int{"stew": 5}},
	})
	defer stop()

	events := capturePayWithItemEvents(t, w)

	_, err := w.Send(sim.PayWithItem("alice", "Bob", "stew", 1, 4, false, nil, nil, 0, 0, "", time.Now().UTC()))
	if err == nil || !strings.Contains(err.Error(), "insufficient coins") {
		t.Fatalf("want insufficient-coins error, got %v", err)
	}

	// No pending entry minted — the fast-fail rejects before nextLedgerSeq.
	if ledger := readPayLedger(t, w); len(ledger) != 0 {
		t.Errorf("ledger should be empty after fast-fail, got %d entries", len(ledger))
	}
	// Seller's warrant never fires.
	if len(events.Offer) != 0 {
		t.Errorf("PayOfferReceived emitted on fast-fail: %v", events.Offer)
	}
	// Buyer's coins untouched.
	if got := w.Published().Actors["alice"].Coins; got != 3 {
		t.Errorf("alice.Coins = %d, want 3 (no movement on fast-fail)", got)
	}
}

// TestPayWithItem_InsufficientFunds_SteerNamesAffordableQuantity (ZBBS-HOME-459):
// the coin-shortfall steer names the quantity the purse covers at the offered
// unit price, pointing the model at the QUANTITY lever rather than the old
// "offer fewer coins" (which had the buyer drop coins, keep the quantity, and
// re-offer underpriced — the John Ellis 25-meat-on-248-coins case).
func TestPayWithItem_InsufficientFunds_SteerNamesAffordableQuantity(t *testing.T) {
	cases := []struct {
		name               string
		coins, qty, amount int
		want               string
	}{
		// 248 coins, 25 units at 250 → affords 24; steer points at the quantity.
		{"affords_some", 248, 25, 250, "you can afford 24"},
		// Nearly broke — can't cover even one unit at this price.
		{"affords_none", 2, 1, 10, "can't afford even one"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
				{id: "alice", displayName: "Alice", kind: sim.KindNPCShared, huddleID: "h1", coins: tc.coins},
				{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1", inventory: map[sim.ItemKind]int{"stew": 100}},
			})
			defer stop()
			_, err := w.Send(sim.PayWithItem("alice", "Bob", "stew", tc.qty, tc.amount, false, nil, nil, 0, 0, "", time.Now().UTC()))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
			if strings.Contains(err.Error(), "offer fewer coins") {
				t.Errorf("old wrong-lever steer still present: %v", err)
			}
		})
	}
}

func TestPayWithItem_SlowPath_NumericGates(t *testing.T) {
	cases := []struct {
		name   string
		amount int
		qty    int
		want   string
	}{
		{"zero_amount", 0, 1, "must include coins or goods"},
		{"negative_amount", -5, 1, "cannot be negative"},
		{"over_max_amount", sim.MaxPayWithItemAmount + 1, 1, "exceeds maximum"},
		{"zero_qty", 1, 0, "at least 1"},
		{"negative_qty", 1, -3, "at least 1"},
		{"over_max_qty", 1, sim.MaxPayWithItemQty + 1, "exceeds maximum"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
				{id: "alice", displayName: "Alice", kind: sim.KindNPCShared, huddleID: "h1", coins: 10},
				{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1", inventory: map[sim.ItemKind]int{"stew": 5}},
			})
			defer stop()
			_, err := w.Send(sim.PayWithItem("alice", "Bob", "stew", tc.qty, tc.amount, false, nil, nil, 0, 0, "", time.Now().UTC()))
			if err == nil {
				t.Fatalf("want error, got nil")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestPayWithItem_SlowPath_BuyerWalkInFlight(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCShared, huddleID: "h1", coins: 10, moveInFlight: true},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1", inventory: map[sim.ItemKind]int{"stew": 5}},
	})
	defer stop()
	_, err := w.Send(sim.PayWithItem("alice", "Bob", "stew", 1, 4, false, nil, nil, 0, 0, "", time.Now().UTC()))
	if err == nil || !strings.Contains(err.Error(), "walking") {
		t.Fatalf("want walking error, got %v", err)
	}
}

func TestPayWithItem_SlowPath_BuyerNoHuddle(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCShared, coins: 10},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1", inventory: map[sim.ItemKind]int{"stew": 5}},
	})
	defer stop()
	_, err := w.Send(sim.PayWithItem("alice", "Bob", "stew", 1, 4, false, nil, nil, 0, 0, "", time.Now().UTC()))
	if err == nil || !strings.Contains(err.Error(), "not in a conversation") {
		t.Fatalf("want no-huddle error, got %v", err)
	}
}

func TestPayWithItem_SlowPath_SellerNotInHuddle(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCShared, huddleID: "h1", coins: 10},
		{id: "carol", displayName: "Carol", kind: sim.KindNPCShared, huddleID: "h1"}, // filler
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, inventory: map[sim.ItemKind]int{"stew": 5}},
	})
	defer stop()
	_, err := w.Send(sim.PayWithItem("alice", "Bob", "stew", 1, 4, false, nil, nil, 0, 0, "", time.Now().UTC()))
	if err == nil || !strings.Contains(err.Error(), "no one named") {
		t.Fatalf("want no-such-peer error, got %v", err)
	}
}

func TestPayWithItem_SlowPath_SelfReject(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCShared, huddleID: "h1", coins: 10},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1"},
	})
	defer stop()
	// Buyer's own DisplayName is excluded from peer scan → "no one named" framing.
	_, err := w.Send(sim.PayWithItem("alice", "Alice", "stew", 1, 4, false, nil, nil, 0, 0, "", time.Now().UTC()))
	if err == nil || !strings.Contains(err.Error(), "no one named") {
		t.Fatalf("want self-reject (no-one-named) error, got %v", err)
	}
}

func TestPayWithItem_SlowPath_AmbiguousSeller(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCShared, huddleID: "h1", coins: 10},
		{id: "bob1", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1"},
		{id: "bob2", displayName: "bob", kind: sim.KindNPCShared, huddleID: "h1"}, // case-insensitive duplicate
	})
	defer stop()
	_, err := w.Send(sim.PayWithItem("alice", "Bob", "stew", 1, 4, false, nil, nil, 0, 0, "", time.Now().UTC()))
	if err == nil || !strings.Contains(err.Error(), "more than one") {
		t.Fatalf("want ambiguous error, got %v", err)
	}
}

func TestPayWithItem_SlowPath_UnknownItem(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCShared, huddleID: "h1", coins: 10},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1"},
	})
	defer stop()
	_, err := w.Send(sim.PayWithItem("alice", "Bob", "fizzbuzz", 1, 4, false, nil, nil, 0, 0, "", time.Now().UTC()))
	if err == nil || !strings.Contains(err.Error(), "unknown item kind") {
		t.Fatalf("want unknown-item error, got %v", err)
	}
}

func TestPayWithItem_SlowPath_TooManyConsumers(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCShared, huddleID: "h1", coins: 100},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1", inventory: map[sim.ItemKind]int{"stew": 100}},
	})
	defer stop()
	too := make([]string, sim.MaxPayWithItemConsumers+1)
	for i := range too {
		too[i] = "Alice"
	}
	_, err := w.Send(sim.PayWithItem("alice", "Bob", "stew", 1, 4, false, too, nil, 0, 0, "", time.Now().UTC()))
	if err == nil || !strings.Contains(err.Error(), "too many consumers") {
		t.Fatalf("want too-many-consumers error, got %v", err)
	}
}

func TestPayWithItem_SlowPath_ConsumerRules(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCShared, huddleID: "h1", coins: 100},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1", inventory: map[sim.ItemKind]int{"stew": 100}},
		{id: "carol", displayName: "Carol", kind: sim.KindNPCShared, huddleID: "h1"},
		{id: "outside", displayName: "Outside", kind: sim.KindNPCShared}, // not in huddle
	})
	defer stop()
	cases := []struct {
		name      string
		consumers []string
		want      string
	}{
		{"seller_as_consumer", []string{"Bob"}, "the seller can't be a consumer"},
		{"missing_name", []string{"Phantom"}, "no one named"},
		{"outside_huddle", []string{"Outside"}, "no one named"},
		{"duplicate", []string{"Carol", "Carol"}, "more than once"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := w.Send(sim.PayWithItem("alice", "Bob", "stew", 1, 4, false, tc.consumers, nil, 0, 0, "", time.Now().UTC()))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
}

func TestPayWithItem_SlowPath_GroupOrder(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCShared, huddleID: "h1", coins: 100},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1", inventory: map[sim.ItemKind]int{"stew": 10}},
		{id: "carol", displayName: "Carol", kind: sim.KindNPCShared, huddleID: "h1"},
	})
	defer stop()
	events := capturePayWithItemEvents(t, w)
	res, err := w.Send(sim.PayWithItem("alice", "Bob", "stew", 1, 6, false, []string{"Alice", "Carol"}, nil, 0, 0, "", time.Now().UTC()))
	if err != nil {
		t.Fatalf("PayWithItem: %v", err)
	}
	result := res.(sim.PayWithItemResult)
	if len(events.Offer) != 1 {
		t.Fatalf("PayOfferReceived = %d, want 1", len(events.Offer))
	}
	if got := events.Offer[0].ConsumerIDs; len(got) != 2 || got[0] != "alice" || got[1] != "carol" {
		t.Errorf("ConsumerIDs = %v, want [alice carol]", got)
	}
	// ConsumerIDs preserved on the ledger entry too.
	ledger := readPayLedger(t, w)
	entry := ledger[result.LedgerID]
	if len(entry.ConsumerIDs) != 2 {
		t.Errorf("entry.ConsumerIDs = %v, want 2 entries", entry.ConsumerIDs)
	}
}

// ============================================================
// PayWithItem — in_response_to chain validation
// ============================================================

func TestPayWithItem_InResponseTo_HappyPath(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCShared, huddleID: "h1", coins: 100},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1", inventory: map[sim.ItemKind]int{"stew": 5}},
	})
	defer stop()

	// Seed a countered parent ledger.
	at := time.Now().UTC()
	seedLedgerEntry(t, w, sim.PayLedgerEntry{
		ID:            42,
		BuyerID:       "alice",
		SellerID:      "bob",
		ItemKind:      "stew",
		Qty:           1,
		Amount:        4,
		State:         sim.PayLedgerStateCountered,
		CounterAmount: 6,
		CreatedAt:     at.Add(-2 * time.Minute),
		ResolvedAt:    at.Add(-time.Minute),
		SceneID:       "sc1",
		HuddleID:      "h1",
		Depth:         0,
	})

	events := capturePayWithItemEvents(t, w)

	res, err := w.Send(sim.PayWithItem("alice", "Bob", "stew", 1, 6, false, nil, nil, 0, 42, "", at))
	if err != nil {
		t.Fatalf("PayWithItem in_response_to: %v", err)
	}
	result := res.(sim.PayWithItemResult)
	ledger := readPayLedger(t, w)
	child, ok := ledger[result.LedgerID]
	if !ok {
		t.Fatalf("child ledger entry missing")
	}
	if child.ParentID != 42 {
		t.Errorf("ParentID = %d, want 42", child.ParentID)
	}
	if child.Depth != 1 {
		t.Errorf("Depth = %d, want 1", child.Depth)
	}
	// ZBBS-WORK-320: the chain depth must propagate onto the emitted
	// PayOfferReceived so the seller's pay-offer warrant (and thus gateTools'
	// counter_pay depth gate) sees it without a ledger lookup.
	if len(events.Offer) != 1 {
		t.Fatalf("PayOfferReceived count = %d, want 1", len(events.Offer))
	}
	if events.Offer[0].Depth != 1 {
		t.Errorf("PayOfferReceived.Depth = %d, want 1 (must mirror child entry depth)", events.Offer[0].Depth)
	}
}

func TestPayWithItem_InResponseTo_Gates(t *testing.T) {
	at := time.Now().UTC()
	// Build a fresh world per case — the in_response_to gates mutate
	// the parent ledger in shapes that don't compose across cases.
	type setupFn func(t *testing.T, w *sim.World)
	cases := []struct {
		name  string
		setup setupFn
		want  string
	}{
		{
			name: "parent_missing",
			setup: func(t *testing.T, w *sim.World) {
				// no entry seeded; parent_id=99 doesn't exist
			},
			want: "parent ledger 99 not found",
		},
		{
			name: "parent_not_countered",
			setup: func(t *testing.T, w *sim.World) {
				seedLedgerEntry(t, w, sim.PayLedgerEntry{
					ID: 99, BuyerID: "alice", SellerID: "bob",
					ItemKind: "stew", Qty: 1, Amount: 4,
					State:   sim.PayLedgerStatePending,
					SceneID: "sc1", HuddleID: "h1",
				})
			},
			want: "is not countered",
		},
		{
			name: "wrong_buyer",
			setup: func(t *testing.T, w *sim.World) {
				seedLedgerEntry(t, w, sim.PayLedgerEntry{
					ID: 99, BuyerID: "stranger", SellerID: "bob",
					ItemKind: "stew", Qty: 1, Amount: 4,
					State:      sim.PayLedgerStateCountered,
					ResolvedAt: at.Add(-time.Minute),
					SceneID:    "sc1", HuddleID: "h1",
				})
			},
			want: "isn't your offer",
		},
		{
			name: "wrong_seller",
			setup: func(t *testing.T, w *sim.World) {
				seedLedgerEntry(t, w, sim.PayLedgerEntry{
					ID: 99, BuyerID: "alice", SellerID: "someone-else",
					ItemKind: "stew", Qty: 1, Amount: 4,
					State:      sim.PayLedgerStateCountered,
					ResolvedAt: at.Add(-time.Minute),
					SceneID:    "sc1", HuddleID: "h1",
				})
			},
			want: "different seller",
		},
		{
			name: "too_old",
			setup: func(t *testing.T, w *sim.World) {
				seedLedgerEntry(t, w, sim.PayLedgerEntry{
					ID: 99, BuyerID: "alice", SellerID: "bob",
					ItemKind: "stew", Qty: 1, Amount: 4,
					State:      sim.PayLedgerStateCountered,
					ResolvedAt: at.Add(-2 * time.Hour),
					SceneID:    "sc1", HuddleID: "h1",
				})
			},
			want: "too old",
		},
		{
			name: "already_answered",
			setup: func(t *testing.T, w *sim.World) {
				seedLedgerEntry(t, w, sim.PayLedgerEntry{
					ID: 99, BuyerID: "alice", SellerID: "bob",
					ItemKind: "stew", Qty: 1, Amount: 4,
					State:      sim.PayLedgerStateCountered,
					ResolvedAt: at.Add(-time.Minute),
					SceneID:    "sc1", HuddleID: "h1",
				})
				// Child already exists with ParentID=99.
				seedLedgerEntry(t, w, sim.PayLedgerEntry{
					ID: 100, BuyerID: "alice", SellerID: "bob",
					ItemKind: "stew", Qty: 1, Amount: 5,
					State:    sim.PayLedgerStatePending,
					ParentID: 99,
					SceneID:  "sc1", HuddleID: "h1",
				})
			},
			want: "already been answered",
		},
		{
			name: "depth_cap",
			setup: func(t *testing.T, w *sim.World) {
				// Parent already at the chain depth limit — a response
				// would push past MaxPayCounterChainDepth.
				seedLedgerEntry(t, w, sim.PayLedgerEntry{
					ID: 99, BuyerID: "alice", SellerID: "bob",
					ItemKind: "stew", Qty: 1, Amount: 4,
					State:      sim.PayLedgerStateCountered,
					ResolvedAt: at.Add(-time.Minute),
					Depth:      sim.MaxPayCounterChainDepth,
					SceneID:    "sc1", HuddleID: "h1",
				})
			},
			want: "depth limit",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
				{id: "alice", displayName: "Alice", kind: sim.KindNPCShared, huddleID: "h1", coins: 100},
				{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1", inventory: map[sim.ItemKind]int{"stew": 5}},
			})
			defer stop()
			tc.setup(t, w)
			_, err := w.Send(sim.PayWithItem("alice", "Bob", "stew", 1, 5, false, nil, nil, 0, 99, "", at))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
}

// TestPayWithItem_QuoteAndInResponseTo_Rejected — the substrate guard for
// the conflicting offer-mode bug (code_review round 1): a quote_id fast-path
// accept and an in_response_to counter-chain response are mutually exclusive
// lifecycle intents. The pc/pay handler rejects this at 400, but NPC/tool
// callers reach PayWithItem directly, so the command enforces it too. The
// guard fires before any world-state lookup, so no huddle/scene/quote/ledger
// seeding is needed.
func TestPayWithItem_QuoteAndInResponseTo_Rejected(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCShared, huddleID: "h1", coins: 100},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1", inventory: map[sim.ItemKind]int{"stew": 5}},
	})
	defer stop()

	_, err := w.Send(sim.PayWithItem("alice", "Bob", "stew", 1, 4, false, nil, nil, 7, 42, "", time.Now().UTC()))
	if err == nil || !strings.Contains(err.Error(), "not both") {
		t.Fatalf("want conflicting-mode rejection, got %v", err)
	}
}

// TestPayWithItem_InResponseTo_DepthIncrementReachesCap proves the PRODUCTION
// increment path actually reaches and stops at the cap (code_review round 1 —
// the depth_cap case in TestPayWithItem_InResponseTo_Gates only seeds a parent
// already at the cap, which doesn't exercise child.Depth = parent.Depth+1). A
// real PayWithItem response to a depth-(cap-1) parent mints a child at exactly
// the cap; responding to THAT child then fails the depth gate.
func TestPayWithItem_InResponseTo_DepthIncrementReachesCap(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCShared, huddleID: "h1", coins: 100},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1", inventory: map[sim.ItemKind]int{"stew": 5}},
	})
	defer stop()

	at := time.Now().UTC()
	capDepth := sim.MaxPayCounterChainDepth

	// Parent one below the cap, countered and answerable.
	seedLedgerEntry(t, w, sim.PayLedgerEntry{
		ID: 50, BuyerID: "alice", SellerID: "bob",
		ItemKind: "stew", Qty: 1, Amount: 4,
		State:         sim.PayLedgerStateCountered,
		CounterAmount: 6,
		ResolvedAt:    at.Add(-time.Minute),
		SceneID:       "sc1", HuddleID: "h1",
		Depth: capDepth - 1,
	})

	// Real response → child minted at exactly the cap (parent.Depth + 1).
	res, err := w.Send(sim.PayWithItem("alice", "Bob", "stew", 1, 6, false, nil, nil, 0, 50, "", at))
	if err != nil {
		t.Fatalf("PayWithItem in_response_to (depth %d): %v", capDepth-1, err)
	}
	childID := res.(sim.PayWithItemResult).LedgerID
	child := readPayLedger(t, w)[childID]
	if child.Depth != capDepth {
		t.Fatalf("child Depth = %d, want %d (cap)", child.Depth, capDepth)
	}

	// Flip the freshly-minted child to a countered, answerable parent at the
	// cap depth — then a response to it must trip the depth gate.
	seedLedgerEntry(t, w, sim.PayLedgerEntry{
		ID: childID, BuyerID: "alice", SellerID: "bob",
		ItemKind: "stew", Qty: 1, Amount: 6,
		State:         sim.PayLedgerStateCountered,
		CounterAmount: 8,
		ResolvedAt:    at.Add(-time.Minute),
		SceneID:       "sc1", HuddleID: "h1",
		Depth: capDepth,
	})

	_, err = w.Send(sim.PayWithItem("alice", "Bob", "stew", 1, 8, false, nil, nil, 0, childID, "", at))
	if err == nil || !strings.Contains(err.Error(), "depth limit") {
		t.Fatalf("want depth-limit rejection at cap depth %d, got %v", capDepth, err)
	}
}

// ============================================================
// PayWithItem — fast path
// ============================================================

// buildFastPathFixture seeds a world with alice (buyer), bob (seller
// with stock + coins), an active matching quote at quoteID, and returns
// the world plus the seed timestamp. Tests override individual
// predicates by tweaking the world before sending PayWithItem.
func buildFastPathFixture(t *testing.T, quoteID sim.QuoteID) (*sim.World, func(), time.Time) {
	t.Helper()
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCShared, huddleID: "h1", coins: 50},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1", inventory: map[sim.ItemKind]int{"stew": 5}},
	})
	at := time.Now().UTC()
	seedQuote(t, w, sim.SceneQuote{
		ID:        quoteID,
		SceneID:   "sc1",
		SellerID:  "bob",
		Lines:     []sim.QuoteLine{{ItemKind: "stew", Qty: 1}},
		Amount:    4,
		State:     sim.SceneQuoteStateActive,
		CreatedAt: at,
		ExpiresAt: at.Add(10 * time.Minute),
	})
	return w, stop, at
}

// TestPayWithItem_SellerDrainedUnderStandingQuote_ReconcileFrontsAcceptGate
// (LLM-409): when a seller spends the quoted goods out from under his own
// standing lot, the pre-publish coverage reconcile flips the lot to shortfall
// BEFORE any buyer take reaches the accept-time stock gate, so a stale take is
// rejected as a gone lot rather than an insufficient-stock error. This is the
// public-API integration counterpart to the reconcile unit test in
// scene_quote_reconcile_test.go. The accept-time stock gate remains as
// defense-in-depth, and the buyer's out-of-stock experiential memory
// (ZBBS-HOME-363) is still captured on the slow/ledger path — see
// TestOutOfStock_RecordsOnInsufficientStock.
func TestPayWithItem_SellerDrainedUnderStandingQuote_ReconcileFrontsAcceptGate(t *testing.T) {
	w, stop, at := buildFastPathFixture(t, 7)
	defer stop()
	// Seller spends his last stew out from under the quote he posted; the reconcile
	// runs on this command and flips the lot to shortfall.
	mustSend(t, w, func(world *sim.World) {
		delete(world.Actors["bob"].Inventory, "stew")
	})

	var state sim.SceneQuoteState
	mustSend(t, w, func(world *sim.World) {
		state = world.Quotes[7].State
	})
	if state != sim.SceneQuoteStateShortfall {
		t.Fatalf("quote state = %q, want shortfall (reconcile should flip an uncoverable standing lot)", state)
	}

	_, err := w.Send(sim.PayWithItem("alice", "Bob", "stew", 1, 4, false, nil, nil, 7, 0, "", at))
	if err == nil || !strings.Contains(err.Error(), "no longer active") {
		t.Fatalf("want a 'no longer active' rejection of the stale take, got %v", err)
	}
}

func TestPayWithItem_FastPath_HappyPath_Takeaway(t *testing.T) {
	w, stop, at := buildFastPathFixture(t, 7)
	defer stop()
	events := capturePayWithItemEvents(t, w)

	// ZBBS-WORK-405: the eat-here clamp now applies to NPC buyers too, so a
	// takeaway test needs the portable kind — bread, mirroring the live data
	// split (bread carries, stew doesn't).
	mustSend(t, w, func(world *sim.World) {
		world.Actors["bob"].Inventory["bread"] = 5
	})
	seedQuote(t, w, sim.SceneQuote{
		ID: 8, SceneID: "sc1", SellerID: "bob",
		Lines: []sim.QuoteLine{{ItemKind: "bread", Qty: 1}}, Amount: 4, State: sim.SceneQuoteStateActive,
		CreatedAt: at, ExpiresAt: at.Add(10 * time.Minute),
	})

	res, err := w.Send(sim.PayWithItem("alice", "Bob", "bread", 1, 4, false, nil, nil, 8, 0, "", at))
	if err != nil {
		t.Fatalf("PayWithItem fast-path: %v", err)
	}
	result := res.(sim.PayWithItemResult)
	if !result.FastPath {
		t.Error("FastPath = false")
	}
	if result.State != sim.PayLedgerStateAccepted {
		t.Errorf("State = %q, want accepted", result.State)
	}
	// ZBBS-HOME-436: a takeaway settle reports the handover, no meal.
	if !result.TookHome {
		t.Error("TookHome = false, want true (physical takeaway delivered at accept)")
	}
	if result.BuyerAte != 0 || result.KeptToInventory != 0 || result.Booked {
		t.Errorf("takeaway settle should carry no meal/booking: %+v", result)
	}

	// No PayOfferReceived on fast path; PayWithItemResolved{Accepted}
	// is the only resolution event.
	if len(events.Offer) != 0 {
		t.Errorf("PayOfferReceived emitted on fast path: %v", events.Offer)
	}
	if len(events.Resolved) != 1 {
		t.Fatalf("PayWithItemResolved = %d, want 1", len(events.Resolved))
	}
	if events.Resolved[0].TerminalState != sim.PayTerminalStateAccepted {
		t.Errorf("TerminalState = %q, want accepted", events.Resolved[0].TerminalState)
	}
	// ZBBS-WORK-420: the fast path IS the instant quote-take, so the resolution
	// flags it for the client's "you took their offer" copy.
	if !events.Resolved[0].BuyerTookQuote {
		t.Errorf("BuyerTookQuote = false, want true (fast-path quote-take)")
	}

	// Coin transfer landed. ZBBS-HOME-398: physical takeaway is delivered to
	// the buyer at accept — the goods move now, and the Order is minted then
	// immediately flipped to Delivered (it stays in the map as a Delivered row
	// so its durable pay_ledger row persists for the price-book seed).
	snap := w.Published()
	if got := snap.Actors["alice"].Coins; got != 46 {
		t.Errorf("alice.Coins = %d, want 46", got)
	}
	if got := snap.Actors["bob"].Coins; got != 4 {
		t.Errorf("bob.Coins = %d, want 4", got)
	}
	a := readHoldings(t, w, "alice")
	b := readHoldings(t, w, "bob")
	if a.inv["bread"] != 1 {
		t.Errorf("alice.bread = %d, want 1 (physical takeaway delivered at accept)", a.inv["bread"])
	}
	if b.inv["bread"] != 4 {
		t.Errorf("bob.bread = %d, want 4 (physical takeaway delivered at accept)", b.inv["bread"])
	}
	// Order minted then immediately delivered (never left Ready).
	var foundOrder *sim.Order
	for _, o := range snap.Orders {
		if o != nil && o.SellerID == "bob" && o.BuyerID == "alice" {
			foundOrder = o
			break
		}
	}
	if foundOrder == nil {
		t.Fatalf("no Order recorded at fast-path accept; snapshot.Orders = %+v", snap.Orders)
	}
	if foundOrder.State != sim.OrderStateDelivered {
		t.Errorf("order State = %q, want delivered (immediate handover)", foundOrder.State)
	}

	// No ItemConsumed events on takeaway.
	if len(events.Consumed) != 0 {
		t.Errorf("ItemConsumed on takeaway: %v", events.Consumed)
	}
}

func TestPayWithItem_FastPath_HappyPath_ConsumeNow(t *testing.T) {
	w, stop, at := buildFastPathFixture(t, 7)
	defer stop()
	// Update quote to ConsumeNow.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Quotes[7].ConsumeNow = true
		return nil, nil
	}}); err != nil {
		t.Fatalf("update quote: %v", err)
	}
	events := capturePayWithItemEvents(t, w)
	res, err := w.Send(sim.PayWithItem("alice", "Bob", "stew", 1, 4, true, nil, nil, 7, 0, "", at))
	if err != nil {
		t.Fatalf("PayWithItem fast-path consume_now: %v", err)
	}
	if !res.(sim.PayWithItemResult).FastPath {
		t.Error("FastPath = false")
	}
	// ZBBS-HOME-436: the settle reports the buyer's meal and post-meal felt
	// state. Alice's needs are all zero here, so the meal leaves her below
	// the awareness floor — sated, nothing left to voice.
	result := res.(sim.PayWithItemResult)
	if result.BuyerAte != 1 {
		t.Errorf("BuyerAte = %d, want 1 (implicit buyer-consumer)", result.BuyerAte)
	}
	if result.SatisfiesNeed != "hunger" {
		t.Errorf("SatisfiesNeed = %q, want hunger (stew's primary restore)", result.SatisfiesNeed)
	}
	if result.FeltAfter != "" {
		t.Errorf("FeltAfter = %q, want empty (sated)", result.FeltAfter)
	}
	if len(events.Consumed) != 1 {
		t.Fatalf("ItemConsumed = %d, want 1 (buyer is implicit consumer)", len(events.Consumed))
	}
	if events.Consumed[0].ActorID != "alice" {
		t.Errorf("ItemConsumed actor = %q, want alice", events.Consumed[0].ActorID)
	}
	// Items NOT added to alice's inventory (consume_now).
	snap := w.Published()
	if got := snap.Actors["alice"].InventoryHash; got != 0 {
		t.Errorf("alice.InventoryHash = %d, want 0 (consumed, not stocked)", got)
	}
}

// LLM-188: a fast-path (quote-take) consume_now buy whose needs-clamp ate fewer
// than purchased records the pocketed surplus on the ledger entry — the
// quote-take is the live repro path (Anne's 5 blueberries at PW Apothecary, ate
// 1, kept 4). The settled-offer perception line reads KeptUnits to reconcile
// the eaten-vs-kept split against the buyer's carried inventory.
func TestPayWithItem_FastPath_ConsumeNow_RecordsKeptUnits(t *testing.T) {
	w, stop, at := buildFastPathFixture(t, 7)
	defer stop()
	mustSend(t, w, func(world *sim.World) {
		world.Quotes[7].ConsumeNow = true
		world.Quotes[7].Lines = []sim.QuoteLine{{ItemKind: "stew", Qty: 5}}
		world.Quotes[7].Amount = 10
		world.Actors["bob"].Inventory["stew"] = 5
		// hunger 2 vs stew Immediate 4 → ceil(2/4)=1 eaten, 4 pocketed.
		world.Actors["alice"].Needs = map[sim.NeedKey]int{"hunger": 2}
	})

	res, err := w.Send(sim.PayWithItem("alice", "Bob", "stew", 5, 10, true, nil, nil, 7, 0, "", at))
	if err != nil {
		t.Fatalf("PayWithItem fast-path consume_now: %v", err)
	}
	result := res.(sim.PayWithItemResult)
	if result.BuyerAte != 1 || result.KeptToInventory != 4 {
		t.Fatalf("BuyerAte/KeptToInventory = %d/%d, want 1/4", result.BuyerAte, result.KeptToInventory)
	}
	if e, ok := readPayLedger(t, w)[result.LedgerID]; !ok || e.KeptUnits != 4 {
		t.Errorf("ledger entry KeptUnits = %d (present=%v), want 4 (clamp surplus recorded)", e.KeptUnits, ok)
	}
}

// TestPayWithItem_FastPath_ConsumeNow_StillHungryFelt (ZBBS-HOME-436): when
// one unit doesn't clear the buyer's need, the settle result voices the
// remaining felt state from LIVE post-commit needs — the within-tick
// perception body is frozen at tick-start values and cannot. A deep-red
// hunger minus stew's immediate 4 stays red, so FeltAfter carries the red
// label and the model has an honest within-tick reason to buy once more —
// and, when FeltAfter later empties, to stop.
func TestPayWithItem_FastPath_ConsumeNow_StillHungryFelt(t *testing.T) {
	w, stop, at := buildFastPathFixture(t, 7)
	defer stop()
	mustSend(t, w, func(world *sim.World) {
		world.Quotes[7].ConsumeNow = true
		world.Actors["alice"].Needs = map[sim.NeedKey]int{"hunger": sim.NeedMax}
	})

	res, err := w.Send(sim.PayWithItem("alice", "Bob", "stew", 1, 4, true, nil, nil, 7, 0, "", at))
	if err != nil {
		t.Fatalf("PayWithItem fast-path consume_now: %v", err)
	}
	result := res.(sim.PayWithItemResult)
	if result.BuyerAte != 1 {
		t.Errorf("BuyerAte = %d, want 1", result.BuyerAte)
	}
	if result.SatisfiesNeed != "hunger" {
		t.Errorf("SatisfiesNeed = %q, want hunger", result.SatisfiesNeed)
	}
	// NeedMax (24) - stew immediate (4) = 20, still at or above the default
	// red threshold — the red label, not mild.
	if result.FeltAfter != "hungry" {
		t.Errorf("FeltAfter = %q, want %q", result.FeltAfter, "hungry")
	}
}

func TestPayWithItem_FastPath_StrictRejectPredicates(t *testing.T) {
	type tweakFn func(t *testing.T, w *sim.World, at time.Time)
	cases := []struct {
		name  string
		tweak tweakFn
		// override args from the default {stew, 1, 4, false, nil}.
		argItem       string
		argQty        int
		argAmount     int
		argConsumeNow bool
		argConsumers  []string
		want          string
	}{
		{
			name: "quote_missing",
			// quote_id 99 not seeded → not found
			want: "not available",
		},
		{
			name: "quote_terminal",
			tweak: func(t *testing.T, w *sim.World, _ time.Time) {
				mustSend(t, w, func(world *sim.World) {
					world.Quotes[7].State = sim.SceneQuoteStateSuperseded
				})
			},
			want: "no longer active",
		},
		{
			name: "quote_expired",
			tweak: func(t *testing.T, w *sim.World, at time.Time) {
				mustSend(t, w, func(world *sim.World) {
					world.Quotes[7].ExpiresAt = at.Add(-time.Minute)
				})
			},
			want: "expired",
		},
		{
			name: "target_buyer_mismatch",
			tweak: func(t *testing.T, w *sim.World, _ time.Time) {
				mustSend(t, w, func(world *sim.World) {
					world.Quotes[7].TargetBuyer = "someone-else"
				})
			},
			want: "is not for you",
		},
		{
			name: "item_mismatch",
			tweak: func(t *testing.T, w *sim.World, _ time.Time) {
				mustSend(t, w, func(world *sim.World) {
					world.Quotes[7].Lines[0].ItemKind = "ale"
					// Back the retargeted good so the LLM-409 coverage reconcile
					// leaves the lot Active and the take reaches the item-mismatch
					// gate (rather than the reconcile pulling an uncoverable lot).
					world.Actors["bob"].Inventory["ale"] = 5
				})
			},
			want: "the item is the good the quote sells",
		},
		{
			name:    "qty_mismatch",
			argItem: "stew", argQty: 2, argAmount: 4,
			want: "is for qty",
		},
		{
			name:    "amount_below_floor",
			argItem: "stew", argQty: 1, argAmount: 3,
			want: "requires at least",
		},
		{
			name: "seller_break",
			tweak: func(t *testing.T, w *sim.World, at time.Time) {
				future := at.Add(time.Minute)
				mustSend(t, w, func(world *sim.World) {
					world.Actors["bob"].BreakUntil = &future
				})
			},
			want: "on break",
		},
		{
			name: "insufficient_stock",
			tweak: func(t *testing.T, w *sim.World, _ time.Time) {
				mustSend(t, w, func(world *sim.World) {
					delete(world.Actors["bob"].Inventory, "stew")
				})
			},
			// LLM-409: draining the seller's stock makes his standing lot
			// uncoverable, so the pre-publish coverage reconcile flips it to
			// shortfall before any take reaches the accept-time stock gate — the
			// take is rejected as a gone lot. The accept-time "doesn't have enough"
			// gate remains as defense-in-depth (see the fixture-level integration
			// test TestPayWithItem_SellerDrainedUnderStandingQuote_ReconcileFrontsAcceptGate).
			want: "no longer active",
		},
		{
			name: "insufficient_coins",
			tweak: func(t *testing.T, w *sim.World, _ time.Time) {
				mustSend(t, w, func(world *sim.World) {
					world.Actors["alice"].Coins = 1
				})
			},
			want: "insufficient coins",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			quoteID := sim.QuoteID(7)
			if tc.name == "quote_missing" {
				quoteID = 99 // unset
			}
			w, stop, at := buildFastPathFixture(t, 7)
			defer stop()
			if tc.tweak != nil {
				tc.tweak(t, w, at)
			}
			item := tc.argItem
			if item == "" {
				item = "stew"
			}
			qty := tc.argQty
			if qty == 0 {
				qty = 1
			}
			amount := tc.argAmount
			if amount == 0 {
				amount = 4
			}
			// Capture coin state AFTER the tweak so cases that mutate
			// alice.Coins as part of setup (insufficient_coins) still
			// check "no transfer happened" rather than "coins == 50".
			before := w.Published()
			beforeCoins := before.Actors["alice"].Coins
			events := capturePayWithItemEvents(t, w)
			_, err := w.Send(sim.PayWithItem("alice", "Bob", item, qty, amount, tc.argConsumeNow, tc.argConsumers, nil, quoteID, 0, "", at))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
			if len(events.Resolved) != 0 || len(events.Offer) != 0 {
				t.Errorf("events emitted on strict-reject: resolved=%v offer=%v", events.Resolved, events.Offer)
			}
			// No coin movement on strict-reject (against post-tweak baseline).
			snap := w.Published()
			if got := snap.Actors["alice"].Coins; got != beforeCoins {
				t.Errorf("alice.Coins changed on strict-reject: %d → %d", beforeCoins, got)
			}
		})
	}
}

// mustSend is a helper that runs fn on the world goroutine with no
// return value. Used by fast-path-tweak setups that mutate world state
// directly.
func mustSend(t *testing.T, w *sim.World, fn func(*sim.World)) {
	t.Helper()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		fn(world)
		return nil, nil
	}}); err != nil {
		t.Fatalf("mustSend: %v", err)
	}
}

// ============================================================
// AcceptPay
// ============================================================

func TestAcceptPay_HappyPath(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCShared, huddleID: "h1", coins: 50},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1", inventory: map[sim.ItemKind]int{"stew": 5}},
	})
	defer stop()
	at := time.Now().UTC()
	seedLedgerEntry(t, w, sim.PayLedgerEntry{
		ID: 1, BuyerID: "alice", SellerID: "bob",
		ItemKind: "stew", Qty: 1, Amount: 4,
		State:     sim.PayLedgerStatePending,
		CreatedAt: at, ExpiresAt: at.Add(3 * time.Minute),
		SceneID: "sc1", HuddleID: "h1",
	})
	events := capturePayWithItemEvents(t, w)

	if _, err := w.Send(sim.AcceptPay("bob", 1, at)); err != nil {
		t.Fatalf("AcceptPay: %v", err)
	}

	ledger := readPayLedger(t, w)
	if got := ledger[1].State; got != sim.PayLedgerStateAccepted {
		t.Errorf("ledger.State = %q, want accepted", got)
	}
	if len(events.Resolved) != 1 || events.Resolved[0].TerminalState != sim.PayTerminalStateAccepted {
		t.Errorf("PayWithItemResolved = %+v", events.Resolved)
	}
	// ZBBS-WORK-420: a slow-path seller-accept of a buyer's pending offer is NOT
	// a quote-take, so the flag stays false (client keeps "they accepted your offer").
	if len(events.Resolved) == 1 && events.Resolved[0].BuyerTookQuote {
		t.Errorf("BuyerTookQuote = true on slow-path AcceptPay, want false")
	}
	snap := w.Published()
	if got := snap.Actors["alice"].Coins; got != 46 {
		t.Errorf("alice.Coins = %d, want 46", got)
	}
	if got := snap.Actors["bob"].Coins; got != 4 {
		t.Errorf("bob.Coins = %d, want 4", got)
	}
}

// TestAcceptPay_StockReservation_PreventsOverselling guards against two
// pending offers against the same 1-stew inventory both succeeding. After
// ZBBS-HOME-398 physical takeaway is delivered at accept, so the first accept
// drains bob's stew immediately and the second accept fails the gate-10 stock
// check on the now-empty inventory. (Pre-397, goods stayed until deliver_order
// and the over-selling guard was outstandingReadyOrderQty's reservation
// accounting; that reservation still protects deferred orders and is covered
// by TestAcceptPay_StockReservation_OverflowFailsClosed.)
func TestAcceptPay_StockReservation_PreventsOverselling(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCShared, huddleID: "h1", coins: 50},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1", inventory: map[sim.ItemKind]int{"stew": 1}},
		{id: "carol", displayName: "Carol", kind: sim.KindNPCShared, huddleID: "h1", coins: 50},
	})
	defer stop()
	at := time.Now().UTC()

	// Two pending offers against the same 1-stew inventory.
	seedLedgerEntry(t, w, sim.PayLedgerEntry{
		ID: 1, BuyerID: "alice", SellerID: "bob",
		ItemKind: "stew", Qty: 1, Amount: 4,
		State:     sim.PayLedgerStatePending,
		CreatedAt: at, ExpiresAt: at.Add(3 * time.Minute),
		SceneID: "sc1", HuddleID: "h1",
	})
	seedLedgerEntry(t, w, sim.PayLedgerEntry{
		ID: 2, BuyerID: "carol", SellerID: "bob",
		ItemKind: "stew", Qty: 1, Amount: 4,
		State:     sim.PayLedgerStatePending,
		CreatedAt: at, ExpiresAt: at.Add(3 * time.Minute),
		SceneID: "sc1", HuddleID: "h1",
	})

	// First accept succeeds and delivers the stew to alice immediately
	// (ZBBS-HOME-398 physical takeaway), draining bob's only stew.
	if _, err := w.Send(sim.AcceptPay("bob", 1, at)); err != nil {
		t.Fatalf("first AcceptPay: %v", err)
	}
	ledger := readPayLedger(t, w)
	if ledger[1].State != sim.PayLedgerStateAccepted {
		t.Errorf("ledger[1].State = %q, want accepted", ledger[1].State)
	}
	a := readHoldings(t, w, "alice")
	b := readHoldings(t, w, "bob")
	if a.inv["stew"] != 1 {
		t.Errorf("alice.stew = %d, want 1 (delivered at accept)", a.inv["stew"])
	}
	if b.inv["stew"] != 0 {
		t.Errorf("bob.stew = %d, want 0 (delivered at accept, inventory drained)", b.inv["stew"])
	}

	// Second accept must FAIL — bob's inventory is now 0, less than the
	// required 1. LLM-302: reached through the accept_pay tool, the shortfall
	// is a retryable ModelFacingError (naming decline/counter) and the entry
	// stays Pending rather than flipping to a silent terminal. Over-selling is
	// still prevented — no transfer fires.
	_, err := w.Send(sim.AcceptPay("bob", 2, at))
	if err == nil || !strings.Contains(err.Error(), "you hold no stew") {
		t.Fatalf("second AcceptPay: want stock-shortfall tool error, got %v", err)
	}
	if !strings.Contains(err.Error(), "decline_pay") || !strings.Contains(err.Error(), "counter_pay") {
		t.Errorf("stock-shortfall error should name decline_pay and counter_pay, got %q", err)
	}
	ledger = readPayLedger(t, w)
	if ledger[2].State != sim.PayLedgerStatePending {
		t.Errorf("ledger[2].State = %q, want pending (tool error leaves the offer open)", ledger[2].State)
	}

	// Carol keeps her coins (atomic transfer didn't fire).
	snap := w.Published()
	if got := snap.Actors["carol"].Coins; got != 50 {
		t.Errorf("carol.Coins = %d, want 50 (no transfer on stock-fail)", got)
	}
}

// TestAcceptPay_StockReservation_OverflowFailsClosed is the PR S6 R2
// code_review regression test. A corrupt Ready Order with math.MaxInt
// Qty must not be allowed to wrap the multiplication arithmetic in
// outstandingReadyOrderQty and reopen the over-selling path. The
// helper saturates to math.MaxInt on overflow; this test verifies
// that propagates through to accept_pay's gate-10 stock check:
// `Inventory[item] - MaxInt < needed` is always true and the gate
// rejects with FailedInsufficientStock.
func TestAcceptPay_StockReservation_OverflowFailsClosed(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCShared, huddleID: "h1", coins: 50},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1", inventory: map[sim.ItemKind]int{"stew": 5}},
		{id: "carol", displayName: "Carol", kind: sim.KindNPCShared, huddleID: "h1", coins: 50},
	})
	defer stop()
	at := time.Now().UTC()

	// Seed a corrupt Ready Order with math.MaxInt qty on bob+stew.
	// Bypasses normal mint validation; simulates a future-bugged
	// repo path loading malformed data.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Orders[42] = &sim.Order{
			ID: 42, State: sim.OrderStateReady,
			BuyerID: "alice", SellerID: "bob",
			Item: "stew", Qty: math.MaxInt,
			ConsumerIDs: []sim.ActorID{"alice"},
			CreatedAt:   at, ExpiresAt: at.Add(time.Hour),
		}
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed corrupt Order: %v", err)
	}

	// A normal pending offer for 1 stew. Bob has 5 stew physically,
	// but the corrupt Order's saturated reservation should fail the
	// stock gate closed.
	seedLedgerEntry(t, w, sim.PayLedgerEntry{
		ID: 7, BuyerID: "carol", SellerID: "bob",
		ItemKind: "stew", Qty: 1, Amount: 4,
		State:     sim.PayLedgerStatePending,
		CreatedAt: at, ExpiresAt: at.Add(3 * time.Minute),
		SceneID: "sc1", HuddleID: "h1",
	})

	// LLM-302: the accept_pay tool path surfaces the saturated-reservation
	// shortfall as a retryable ModelFacingError and leaves the entry Pending —
	// the over-selling protection is unchanged (the accept is refused), only
	// the feedback shape changed from a silent terminal to a tool error.
	_, err := w.Send(sim.AcceptPay("bob", 7, at.Add(time.Second)))
	if err == nil || !strings.Contains(err.Error(), "enough stew") {
		t.Fatalf("AcceptPay: want stock-shortfall tool error (corrupt-Order overflow must fail closed), got %v", err)
	}
	ledger := readPayLedger(t, w)
	if ledger[7].State != sim.PayLedgerStatePending {
		t.Errorf("ledger[7].State = %q, want pending (tool error leaves the offer open; accept still refused)",
			ledger[7].State)
	}
}

func TestAcceptPay_AuthGate_NotSeller(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCShared, huddleID: "h1", coins: 50},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1", inventory: map[sim.ItemKind]int{"stew": 5}},
	})
	defer stop()
	at := time.Now().UTC()
	seedLedgerEntry(t, w, sim.PayLedgerEntry{
		ID: 1, BuyerID: "alice", SellerID: "bob",
		ItemKind: "stew", Qty: 1, Amount: 4,
		State: sim.PayLedgerStatePending, ExpiresAt: at.Add(3 * time.Minute),
		SceneID: "sc1", HuddleID: "h1",
	})
	// Alice (the buyer, NOT the seller) tries to accept — auth idempotent
	// reject. No transition.
	_, err := w.Send(sim.AcceptPay("alice", 1, at))
	if err == nil || !strings.Contains(err.Error(), "only the seller") {
		t.Fatalf("want only-seller error, got %v", err)
	}
	ledger := readPayLedger(t, w)
	if got := ledger[1].State; got != sim.PayLedgerStatePending {
		t.Errorf("state transitioned on auth reject: %q", got)
	}
}

func TestAcceptPay_StateGate_AlreadyTerminal(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCShared, huddleID: "h1", coins: 50},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1", inventory: map[sim.ItemKind]int{"stew": 5}},
	})
	defer stop()
	at := time.Now().UTC()
	seedLedgerEntry(t, w, sim.PayLedgerEntry{
		ID: 1, BuyerID: "alice", SellerID: "bob",
		ItemKind: "stew", Qty: 1, Amount: 4,
		State: sim.PayLedgerStateDeclined, ExpiresAt: at.Add(3 * time.Minute),
		SceneID: "sc1", HuddleID: "h1",
	})
	_, err := w.Send(sim.AcceptPay("bob", 1, at))
	if err == nil || !strings.Contains(err.Error(), "no longer pending") {
		t.Fatalf("want state-gate error, got %v", err)
	}
}

func TestAcceptPay_LedgerNotFound(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1"},
	})
	defer stop()
	_, err := w.Send(sim.AcceptPay("bob", 999, time.Now().UTC()))
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("want not-found, got %v", err)
	}
}

func TestAcceptPay_TerminalFlips(t *testing.T) {
	type setup struct {
		invalidateAfterSeed func(t *testing.T, w *sim.World, at time.Time)
		wantTerminal        sim.PayTerminalState
	}
	cases := map[string]setup{
		"ttl_expired": {
			invalidateAfterSeed: func(t *testing.T, w *sim.World, at time.Time) {
				mustSend(t, w, func(world *sim.World) {
					world.PayLedger[1].ExpiresAt = at.Add(-time.Minute)
				})
			},
			wantTerminal: sim.PayTerminalStateExpired,
		},
		"buyer_left_huddle": {
			invalidateAfterSeed: func(t *testing.T, w *sim.World, _ time.Time) {
				mustSend(t, w, func(world *sim.World) {
					world.Actors["alice"].CurrentHuddleID = ""
					sim.RebuildIndicesForTest(world)
				})
			},
			wantTerminal: sim.PayTerminalStateFailedUnavailable,
		},
		"seller_left_huddle": {
			invalidateAfterSeed: func(t *testing.T, w *sim.World, _ time.Time) {
				mustSend(t, w, func(world *sim.World) {
					world.Actors["bob"].CurrentHuddleID = ""
					sim.RebuildIndicesForTest(world)
				})
			},
			wantTerminal: sim.PayTerminalStateFailedUnavailable,
		},
		"seller_on_break": {
			invalidateAfterSeed: func(t *testing.T, w *sim.World, at time.Time) {
				future := at.Add(time.Minute)
				mustSend(t, w, func(world *sim.World) {
					world.Actors["bob"].BreakUntil = &future
				})
			},
			wantTerminal: sim.PayTerminalStateFailedUnavailable,
		},
		"item_kind_dropped": {
			invalidateAfterSeed: func(t *testing.T, w *sim.World, _ time.Time) {
				mustSend(t, w, func(world *sim.World) {
					delete(world.ItemKinds, "stew")
				})
			},
			wantTerminal: sim.PayTerminalStateFailedUnavailable,
		},
		// NOTE (LLM-302): insufficient_stock is no longer a terminal flip via
		// the accept_pay tool — it surfaces as a retryable ModelFacingError
		// with the entry left Pending. See
		// TestAcceptPay_InsufficientStock_ToolErrorNotTerminal (accept path)
		// and TestCounterPayCoercion_InsufficientStock_FlipsTerminal (the
		// retained counter-coercion backstop).
		"insufficient_funds": {
			invalidateAfterSeed: func(t *testing.T, w *sim.World, _ time.Time) {
				mustSend(t, w, func(world *sim.World) {
					world.Actors["alice"].Coins = 1
				})
			},
			wantTerminal: sim.PayTerminalStateFailedInsufficientFunds,
		},
	}
	for name, s := range cases {
		t.Run(name, func(t *testing.T) {
			w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
				{id: "alice", displayName: "Alice", kind: sim.KindNPCShared, huddleID: "h1", coins: 50},
				{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1", inventory: map[sim.ItemKind]int{"stew": 5}},
			})
			defer stop()
			at := time.Now().UTC()
			seedLedgerEntry(t, w, sim.PayLedgerEntry{
				ID: 1, BuyerID: "alice", SellerID: "bob",
				ItemKind: "stew", Qty: 1, Amount: 4,
				State: sim.PayLedgerStatePending, ExpiresAt: at.Add(3 * time.Minute),
				SceneID: "sc1", HuddleID: "h1",
			})
			s.invalidateAfterSeed(t, w, at)
			// Snapshot the buyer's coin balance AFTER any invalidation
			// tweak — cases that mutate alice.Coins as part of the gate
			// setup (insufficient_funds) still want "no transfer
			// happened" rather than "coins == 50".
			before := w.Published()
			beforeCoins := before.Actors["alice"].Coins
			events := capturePayWithItemEvents(t, w)
			if _, err := w.Send(sim.AcceptPay("bob", 1, at)); err != nil {
				t.Fatalf("AcceptPay: %v", err)
			}
			ledger := readPayLedger(t, w)
			if got := ledger[1].State; sim.PayTerminalState(got) != s.wantTerminal {
				t.Errorf("ledger.State = %q, want %q", got, s.wantTerminal)
			}
			if len(events.Resolved) != 1 || events.Resolved[0].TerminalState != s.wantTerminal {
				t.Errorf("PayWithItemResolved terminal = %+v, want %q", events.Resolved, s.wantTerminal)
			}
			// No coin movement on terminal failures (against post-tweak baseline).
			snap := w.Published()
			if got := snap.Actors["alice"].Coins; got != beforeCoins {
				t.Errorf("alice.Coins moved on terminal failure: %d → %d", beforeCoins, got)
			}
		})
	}
}

// TestAcceptPay_InsufficientStock_ToolErrorNotTerminal is the LLM-302 core:
// a seller who accepts an offer for goods they don't hold gets a retryable
// ModelFacingError naming the shortfall and the legal alternatives — NOT a
// silent failed_insufficient_stock terminal that the weak stateful model
// misreads as agreement. The entry stays Pending, no event is emitted, no
// coins move, and the seller can still decline the still-open offer.
func TestAcceptPay_InsufficientStock_ToolErrorNotTerminal(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCShared, huddleID: "h1", coins: 50},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1"}, // holds no stew
	})
	defer stop()
	at := time.Now().UTC()
	seedLedgerEntry(t, w, sim.PayLedgerEntry{
		ID: 1, BuyerID: "alice", SellerID: "bob",
		ItemKind: "stew", Qty: 1, Amount: 4,
		State: sim.PayLedgerStatePending, ExpiresAt: at.Add(3 * time.Minute),
		SceneID: "sc1", HuddleID: "h1",
	})

	events := capturePayWithItemEvents(t, w)
	_, err := w.Send(sim.AcceptPay("bob", 1, at))
	if err == nil {
		t.Fatalf("AcceptPay on zero stock: want tool error, got nil")
	}
	// The error names the shortfall and both legal next moves (copyable
	// next action, LLM-302).
	for _, want := range []string{"you hold no stew", "decline_pay", "counter_pay"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
	// It reaches the model as a ModelFacingError (echoed to the LLM), not a
	// generic internal error swallowed at dispatch.
	var mfe sim.ModelFacingError
	if !errors.As(err, &mfe) {
		t.Errorf("error is not a sim.ModelFacingError: %T", err)
	}
	// The offer stays Pending — the seller can still act on it.
	if got := readPayLedger(t, w)[1].State; got != sim.PayLedgerStatePending {
		t.Errorf("ledger.State = %q, want pending (tool error must not transition)", got)
	}
	// No resolution event fired (nothing settled).
	if len(events.Resolved) != 0 {
		t.Errorf("PayWithItemResolved emitted on a tool-error accept: %+v", events.Resolved)
	}
	// No coins moved.
	if got := w.Published().Actors["alice"].Coins; got != 50 {
		t.Errorf("alice.Coins = %d, want 50 (no transfer)", got)
	}

	// decline_pay still resolves the still-open offer (DoD: decline works).
	if _, err := w.Send(sim.DeclinePay("bob", 1, "sorry, out of stew", at)); err != nil {
		t.Fatalf("DeclinePay after failed accept: %v", err)
	}
	if got := readPayLedger(t, w)[1].State; got != sim.PayLedgerStateDeclined {
		t.Errorf("ledger.State = %q, want declined", got)
	}
}

// TestAcceptPay_Barter_InsufficientStock_ToolError models the live
// Josiah×Elizabeth episode (pay ledger 869/870/871, 2026-07-06): a proposer
// offers goods for an item the counterparty does not hold. The offer_trade
// front door lowers to a barter PayLedger entry where the counterparty is the
// seller of the wanted item; accepting it with zero stock now returns a tool
// error naming the item, and counter_pay still works on the still-open barter
// (DoD: counter works). Uses seeded catalog kinds (stew/bread/ale) in place of
// the episode's sage/nails, which aren't in the test catalog.
func TestAcceptPay_Barter_InsufficientStock_ToolError(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		// Josiah pays 1 bread; Elizabeth is the seller of 5 stew she lacks.
		{id: "josiah", displayName: "Josiah", kind: sim.KindNPCShared, huddleID: "h1", inventory: map[sim.ItemKind]int{"bread": 1}},
		{id: "elizabeth", displayName: "Elizabeth", kind: sim.KindNPCShared, huddleID: "h1", inventory: map[sim.ItemKind]int{"ale": 3}},
	})
	defer stop()
	at := time.Now().UTC()
	seedLedgerEntry(t, w, sim.PayLedgerEntry{
		ID: 1, BuyerID: "josiah", SellerID: "elizabeth",
		ItemKind: "stew", Qty: 5, Amount: 0,
		PayItems:  []sim.ItemKindQty{{Kind: "bread", Qty: 1}},
		State:     sim.PayLedgerStatePending,
		CreatedAt: at, ExpiresAt: at.Add(3 * time.Minute),
		SceneID: "sc1", HuddleID: "h1",
	})

	_, err := w.Send(sim.AcceptPay("elizabeth", 1, at))
	if err == nil || !strings.Contains(err.Error(), "you hold no stew") {
		t.Fatalf("AcceptPay on zero stew: want 'you hold no stew' tool error, got %v", err)
	}
	if got := readPayLedger(t, w)[1].State; got != sim.PayLedgerStatePending {
		t.Errorf("ledger.State = %q, want pending", got)
	}
	// Nothing moved — Josiah keeps his bread, Elizabeth's pack is unchanged.
	j := readHoldings(t, w, "josiah")
	e := readHoldings(t, w, "elizabeth")
	if j.inv["bread"] != 1 || e.inv["stew"] != 0 {
		t.Errorf("goods moved on failed barter accept: josiah.bread=%d elizabeth.stew=%d", j.inv["bread"], e.inv["stew"])
	}

	// counter_pay still resolves the still-open barter (a goods-bearing counter
	// is a real counter → Countered terminal).
	if _, err := w.Send(sim.CounterPay("elizabeth", 1, 0, []sim.PayItemInput{{Item: "ale", Qty: 1}}, "I've no stew, take ale?", at)); err != nil {
		t.Fatalf("CounterPay after failed accept: %v", err)
	}
	if got := readPayLedger(t, w)[1].State; got != sim.PayLedgerStateCountered {
		t.Errorf("ledger.State = %q, want countered", got)
	}
}

// TestCounterPayCoercion_InsufficientStock_FlipsTerminal pins the retained
// backstop (LLM-302): a stock shortfall reached through counter_pay's
// non-increasing-coercion (a seller "countering" at or below the offered price
// is a yes) is NOT an accept_pay tool call, so it keeps the
// failed_insufficient_stock terminal flip rather than a tool error.
func TestCounterPayCoercion_InsufficientStock_FlipsTerminal(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCShared, huddleID: "h1", coins: 50},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1"}, // holds no stew
	})
	defer stop()
	at := time.Now().UTC()
	// Pure-coin offer (no PayItems) so a non-increasing counter coerces to
	// accept and takes the shared accept path.
	seedLedgerEntry(t, w, sim.PayLedgerEntry{
		ID: 1, BuyerID: "alice", SellerID: "bob",
		ItemKind: "stew", Qty: 1, Amount: 4,
		State: sim.PayLedgerStatePending, ExpiresAt: at.Add(3 * time.Minute),
		SceneID: "sc1", HuddleID: "h1",
	})

	events := capturePayWithItemEvents(t, w)
	// Counter at the offered price (4 <= 4) with no goods → coercion to accept.
	if _, err := w.Send(sim.CounterPay("bob", 1, 4, nil, "deal", at)); err != nil {
		t.Fatalf("CounterPay coercion: %v", err)
	}
	if got := readPayLedger(t, w)[1].State; got != sim.PayLedgerStateFailedInsufficientStock {
		t.Errorf("ledger.State = %q, want failed_insufficient_stock (backstop retained)", got)
	}
	if len(events.Resolved) != 1 || events.Resolved[0].TerminalState != sim.PayTerminalStateFailedInsufficientStock {
		t.Errorf("Resolved = %+v, want one failed_insufficient_stock", events.Resolved)
	}
	// No coins moved.
	if got := w.Published().Actors["alice"].Coins; got != 50 {
		t.Errorf("alice.Coins = %d, want 50 (no transfer on coercion stock-fail)", got)
	}
}

func TestAcceptPay_ConsumerDeparture(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCShared, huddleID: "h1", coins: 50},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1", inventory: map[sim.ItemKind]int{"stew": 10}},
		{id: "carol", displayName: "Carol", kind: sim.KindNPCShared, huddleID: "h1"},
	})
	defer stop()
	at := time.Now().UTC()
	seedLedgerEntry(t, w, sim.PayLedgerEntry{
		ID: 1, BuyerID: "alice", SellerID: "bob",
		ItemKind: "stew", Qty: 1, Amount: 6,
		ConsumerIDs: []sim.ActorID{"alice", "carol"},
		State:       sim.PayLedgerStatePending, ExpiresAt: at.Add(3 * time.Minute),
		SceneID: "sc1", HuddleID: "h1",
	})
	// Carol wanders off mid-pending — drops out of the huddle index.
	mustSend(t, w, func(world *sim.World) {
		world.Actors["carol"].CurrentHuddleID = ""
		sim.RebuildIndicesForTest(world)
	})
	if _, err := w.Send(sim.AcceptPay("bob", 1, at)); err != nil {
		t.Fatalf("AcceptPay: %v", err)
	}
	ledger := readPayLedger(t, w)
	if got := ledger[1].State; got != sim.PayLedgerState(sim.PayTerminalStateFailedUnavailable) {
		t.Errorf("ledger.State = %q, want failed_unavailable", got)
	}
}

func TestAcceptPay_GroupOrder_ConsumeNow(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCShared, huddleID: "h1", coins: 50},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1", inventory: map[sim.ItemKind]int{"stew": 10}},
		{id: "carol", displayName: "Carol", kind: sim.KindNPCShared, huddleID: "h1"},
	})
	defer stop()
	at := time.Now().UTC()
	seedLedgerEntry(t, w, sim.PayLedgerEntry{
		ID: 1, BuyerID: "alice", SellerID: "bob",
		ItemKind: "stew", Qty: 1, Amount: 6, ConsumeNow: true,
		ConsumerIDs: []sim.ActorID{"alice", "carol"},
		State:       sim.PayLedgerStatePending, ExpiresAt: at.Add(3 * time.Minute),
		SceneID: "sc1", HuddleID: "h1",
	})
	events := capturePayWithItemEvents(t, w)
	if _, err := w.Send(sim.AcceptPay("bob", 1, at)); err != nil {
		t.Fatalf("AcceptPay: %v", err)
	}
	// One ItemConsumed per consumer.
	if len(events.Consumed) != 2 {
		t.Fatalf("ItemConsumed = %d, want 2 (alice + carol)", len(events.Consumed))
	}
	snap := w.Published()
	// Stew leaves bob's inventory (2 units total — 1 per consumer).
	// Snapshot stores InventoryHash = sum of qty.
	if got := snap.Actors["bob"].InventoryHash; got != 8 {
		t.Errorf("bob.InventoryHash = %d, want 8 (started 10, sold 2)", got)
	}
	// ConsumeNow: items NOT added to consumer inventory.
	if got := snap.Actors["alice"].InventoryHash; got != 0 {
		t.Errorf("alice.InventoryHash = %d, want 0", got)
	}
	if got := snap.Actors["carol"].InventoryHash; got != 0 {
		t.Errorf("carol.InventoryHash = %d, want 0", got)
	}
	// Coins moved: alice → bob.
	if got := snap.Actors["alice"].Coins; got != 44 {
		t.Errorf("alice.Coins = %d, want 44", got)
	}
	if got := snap.Actors["bob"].Coins; got != 6 {
		t.Errorf("bob.Coins = %d, want 6", got)
	}
}

// ============================================================
// DeclinePay
// ============================================================

func TestDeclinePay_HappyPath(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCShared, huddleID: "h1", coins: 50},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1"},
	})
	defer stop()
	at := time.Now().UTC()
	seedLedgerEntry(t, w, sim.PayLedgerEntry{
		ID: 1, BuyerID: "alice", SellerID: "bob",
		ItemKind: "stew", Qty: 1, Amount: 4,
		State: sim.PayLedgerStatePending, ExpiresAt: at.Add(3 * time.Minute),
		SceneID: "sc1", HuddleID: "h1",
	})
	events := capturePayWithItemEvents(t, w)

	if _, err := w.Send(sim.DeclinePay("bob", 1, "not enough", at)); err != nil {
		t.Fatalf("DeclinePay: %v", err)
	}
	ledger := readPayLedger(t, w)
	if got := ledger[1].State; got != sim.PayLedgerStateDeclined {
		t.Errorf("State = %q, want declined", got)
	}
	if got := ledger[1].Message; got != "not enough" {
		t.Errorf("Message = %q, want %q", got, "not enough")
	}
	if len(events.Resolved) != 1 || events.Resolved[0].TerminalState != sim.PayTerminalStateDeclined {
		t.Errorf("PayWithItemResolved = %+v", events.Resolved)
	}
	// Bidirectional relationship facts — both shared NPCs.
	snap := w.Published()
	if rel := snap.Actors["alice"].Relationships["bob"]; rel == nil || len(rel.SalientFacts) != 1 {
		t.Errorf("alice→bob relationship missing or has wrong fact count: %+v", rel)
	} else if rel.SalientFacts[0].Kind != sim.InteractionPayDeclinedBy {
		t.Errorf("alice→bob fact.Kind = %q, want PayDeclinedBy", rel.SalientFacts[0].Kind)
	}
	if rel := snap.Actors["bob"].Relationships["alice"]; rel == nil || rel.SalientFacts[0].Kind != sim.InteractionDeclinedPay {
		t.Errorf("bob→alice fact = %+v", rel)
	}
}

func TestDeclinePay_AuthAndState(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCShared, huddleID: "h1", coins: 50},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1"},
	})
	defer stop()
	at := time.Now().UTC()
	seedLedgerEntry(t, w, sim.PayLedgerEntry{
		ID: 1, BuyerID: "alice", SellerID: "bob",
		ItemKind: "stew", Qty: 1, Amount: 4,
		State: sim.PayLedgerStatePending, ExpiresAt: at.Add(3 * time.Minute),
		SceneID: "sc1", HuddleID: "h1",
	})
	// Auth: buyer can't decline.
	if _, err := w.Send(sim.DeclinePay("alice", 1, "", at)); err == nil || !strings.Contains(err.Error(), "only the seller") {
		t.Fatalf("want only-seller error, got %v", err)
	}

	// Drive into terminal state then re-attempt decline.
	mustSend(t, w, func(world *sim.World) {
		world.PayLedger[1].State = sim.PayLedgerStateAccepted
	})
	if _, err := w.Send(sim.DeclinePay("bob", 1, "", at)); err == nil || !strings.Contains(err.Error(), "no longer pending") {
		t.Fatalf("want state-gate error, got %v", err)
	}
}

func TestDeclinePay_MessageTruncation(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCShared, huddleID: "h1"},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1"},
	})
	defer stop()
	at := time.Now().UTC()
	seedLedgerEntry(t, w, sim.PayLedgerEntry{
		ID: 1, BuyerID: "alice", SellerID: "bob",
		ItemKind: "stew", Qty: 1, Amount: 4,
		State: sim.PayLedgerStatePending, ExpiresAt: at.Add(3 * time.Minute),
		SceneID: "sc1", HuddleID: "h1",
	})
	overlong := strings.Repeat("a", sim.MaxPayMessageRunes+50)
	if _, err := w.Send(sim.DeclinePay("bob", 1, overlong, at)); err != nil {
		t.Fatalf("DeclinePay: %v", err)
	}
	ledger := readPayLedger(t, w)
	if got := len([]rune(ledger[1].Message)); got != sim.MaxPayMessageRunes {
		t.Errorf("Message rune-len = %d, want %d (truncated)", got, sim.MaxPayMessageRunes)
	}
}

// ============================================================
// CounterPay
// ============================================================

func TestCounterPay_HappyPath(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCShared, huddleID: "h1", coins: 50},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1"},
	})
	defer stop()
	at := time.Now().UTC()
	seedLedgerEntry(t, w, sim.PayLedgerEntry{
		ID: 1, BuyerID: "alice", SellerID: "bob",
		ItemKind: "stew", Qty: 1, Amount: 4,
		State: sim.PayLedgerStatePending, ExpiresAt: at.Add(3 * time.Minute),
		SceneID: "sc1", HuddleID: "h1",
	})
	events := capturePayWithItemEvents(t, w)

	if _, err := w.Send(sim.CounterPay("bob", 1, 6, nil, "how about six", at)); err != nil {
		t.Fatalf("CounterPay: %v", err)
	}
	ledger := readPayLedger(t, w)
	entry := ledger[1]
	if entry.State != sim.PayLedgerStateCountered {
		t.Errorf("State = %q, want countered", entry.State)
	}
	if entry.CounterAmount != 6 {
		t.Errorf("CounterAmount = %d, want 6", entry.CounterAmount)
	}
	if entry.Message != "how about six" {
		t.Errorf("Message = %q", entry.Message)
	}
	// PayCountered emitted (NOT PayWithItemResolved).
	if len(events.Counter) != 1 {
		t.Fatalf("PayCountered = %d, want 1", len(events.Counter))
	}
	if got := events.Counter[0]; got.ParentID != 1 || got.OriginalAmount != 4 || got.CounterAmount != 6 {
		t.Errorf("PayCountered payload = %+v", got)
	}
	if len(events.Resolved) != 0 {
		t.Errorf("PayWithItemResolved emitted on counter: %v", events.Resolved)
	}
	snap := w.Published()
	if rel := snap.Actors["alice"].Relationships["bob"]; rel == nil || rel.SalientFacts[0].Kind != sim.InteractionCounteredBy {
		t.Errorf("alice→bob fact = %+v", rel)
	}
	if rel := snap.Actors["bob"].Relationships["alice"]; rel == nil || rel.SalientFacts[0].Kind != sim.InteractionCountered {
		t.Errorf("bob→alice fact = %+v", rel)
	}
}

func TestCounterPay_Gates(t *testing.T) {
	cases := []struct {
		name          string
		counterAmount int
		caller        sim.ActorID
		preMutate     func(world *sim.World)
		want          string
	}{
		{name: "not_seller", counterAmount: 6, caller: "alice", want: "only the seller"},
		{name: "non_pending", counterAmount: 6, caller: "bob",
			preMutate: func(w *sim.World) { w.PayLedger[1].State = sim.PayLedgerStateExpired },
			want:      "no longer pending"},
		{name: "zero_amount", counterAmount: 0, caller: "bob", want: "must propose coins or goods"},
		{name: "negative_amount", counterAmount: -5, caller: "bob", want: "cannot be negative"},
		{name: "over_max", counterAmount: sim.MaxPayWithItemAmount + 1, caller: "bob", want: "exceeds maximum"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
				{id: "alice", displayName: "Alice", kind: sim.KindNPCShared, huddleID: "h1", coins: 50},
				{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1"},
			})
			defer stop()
			at := time.Now().UTC()
			seedLedgerEntry(t, w, sim.PayLedgerEntry{
				ID: 1, BuyerID: "alice", SellerID: "bob",
				ItemKind: "stew", Qty: 1, Amount: 4,
				State: sim.PayLedgerStatePending, ExpiresAt: at.Add(3 * time.Minute),
				SceneID: "sc1", HuddleID: "h1",
			})
			if tc.preMutate != nil {
				mustSend(t, w, tc.preMutate)
			}
			_, err := w.Send(sim.CounterPay(tc.caller, 1, tc.counterAmount, nil, "", at))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
}

// TestCounterPay_NonIncreasingCoercesToAccept covers the v1 LLM-behavior
// scar: a seller "countering" at or below the buyer's offered amount is
// agreeing, not negotiating, so it resolves as an accept at the OFFERED
// amount rather than recording a counter. Both the equal case (counter ==
// offer) and the below case (volunteered discount) coerce.
func TestCounterPay_NonIncreasingCoercesToAccept(t *testing.T) {
	cases := []struct {
		name          string
		counterAmount int
	}{
		{"equal_to_offer", 4},
		{"below_offer", 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
				{id: "alice", displayName: "Alice", kind: sim.KindNPCShared, huddleID: "h1", coins: 50},
				{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1", inventory: map[sim.ItemKind]int{"stew": 5}},
			})
			defer stop()
			at := time.Now().UTC()
			seedLedgerEntry(t, w, sim.PayLedgerEntry{
				ID: 1, BuyerID: "alice", SellerID: "bob",
				ItemKind: "stew", Qty: 1, Amount: 4,
				State: sim.PayLedgerStatePending, ExpiresAt: at.Add(3 * time.Minute),
				SceneID: "sc1", HuddleID: "h1",
			})
			events := capturePayWithItemEvents(t, w)

			res, err := w.Send(sim.CounterPay("bob", 1, tc.counterAmount, nil, "i'll do it for that", at))
			if err != nil {
				t.Fatalf("CounterPay (coerce): %v", err)
			}
			if got := res.(sim.PayLedgerState); got != sim.PayLedgerStateAccepted {
				t.Errorf("returned state = %q, want accepted", got)
			}
			entry := readPayLedger(t, w)[1]
			if entry.State != sim.PayLedgerStateAccepted {
				t.Errorf("entry.State = %q, want accepted (non-increasing counter coerced)", entry.State)
			}
			// Transfer at the OFFERED amount; the counter is dropped.
			if entry.Amount != 4 {
				t.Errorf("entry.Amount = %d, want 4 (offered amount preserved)", entry.Amount)
			}
			if entry.CounterAmount != 0 {
				t.Errorf("CounterAmount = %d, want 0 (coerced to accept, no counter recorded)", entry.CounterAmount)
			}
			// PayWithItemResolved{Accepted}, NOT PayCountered.
			if len(events.Counter) != 0 {
				t.Errorf("PayCountered emitted on coerced accept: %v", events.Counter)
			}
			if len(events.Resolved) != 1 || events.Resolved[0].TerminalState != sim.PayTerminalStateAccepted {
				t.Fatalf("want 1 PayWithItemResolved{Accepted}, got %+v", events.Resolved)
			}
			// Coins moved at the offered amount (4), not the counter.
			snap := w.Published()
			if got := snap.Actors["alice"].Coins; got != 46 {
				t.Errorf("alice.Coins = %d, want 46", got)
			}
			if got := snap.Actors["bob"].Coins; got != 4 {
				t.Errorf("bob.Coins = %d, want 4", got)
			}
		})
	}
}

// LLM-105: a slow-path accept of a barter offer must carry the buyer's goods leg on
// the resolved event, so the durable `paid` audit row records what actually moved —
// and a 0-coin barter (0 coins + goods) is then distinguishable from a give-away.
// (This is the real barter-settlement path; the coerced-accept path is gated to
// pure-coin haggles via pureCoinHaggle in CounterPay, so it never carries goods.)
func TestAcceptPay_ResolvedCarriesBarterGoods(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCShared, huddleID: "h1", coins: 50, inventory: map[sim.ItemKind]int{"nail": 10}},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1", inventory: map[sim.ItemKind]int{"stew": 5}},
	})
	defer stop()
	at := time.Now().UTC()
	// Buyer's pending offer: 0 coins + 3 nails for 1 stew — the exact 0-coin-but-not-
	// free barter case the audit must tell apart from a give-away.
	seedLedgerEntry(t, w, sim.PayLedgerEntry{
		ID: 1, BuyerID: "alice", SellerID: "bob",
		ItemKind: "stew", Qty: 1, Amount: 0,
		PayItems:  []sim.ItemKindQty{{Kind: "nail", Qty: 3}},
		State:     sim.PayLedgerStatePending,
		ExpiresAt: at.Add(3 * time.Minute),
		SceneID:   "sc1", HuddleID: "h1",
	})
	events := capturePayWithItemEvents(t, w)

	if _, err := w.Send(sim.AcceptPay("bob", 1, at)); err != nil {
		t.Fatalf("AcceptPay: %v", err)
	}
	if len(events.Resolved) != 1 || events.Resolved[0].TerminalState != sim.PayTerminalStateAccepted {
		t.Fatalf("want 1 PayWithItemResolved{Accepted}, got %+v", events.Resolved)
	}
	got := events.Resolved[0].PayItems
	if len(got) != 1 || got[0].Kind != "nail" || got[0].Qty != 3 {
		t.Errorf("resolved.PayItems = %+v, want the buyer's barter goods [{nail 3}] (audit records what moved)", got)
	}
}

// ============================================================
// WithdrawPay
// ============================================================

func TestWithdrawPay_HappyPath(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCShared, huddleID: "h1", coins: 50},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1"},
	})
	defer stop()
	at := time.Now().UTC()
	seedLedgerEntry(t, w, sim.PayLedgerEntry{
		ID: 1, BuyerID: "alice", SellerID: "bob",
		ItemKind: "stew", Qty: 1, Amount: 4,
		State: sim.PayLedgerStatePending, ExpiresAt: at.Add(3 * time.Minute),
		SceneID: "sc1", HuddleID: "h1",
	})
	events := capturePayWithItemEvents(t, w)

	if _, err := w.Send(sim.WithdrawPay("alice", 1, "changed my mind", at)); err != nil {
		t.Fatalf("WithdrawPay: %v", err)
	}
	ledger := readPayLedger(t, w)
	if got := ledger[1].State; got != sim.PayLedgerStateWithdrawnByBuyer {
		t.Errorf("State = %q, want withdrawn_by_buyer", got)
	}
	if got := ledger[1].Message; got != "changed my mind" {
		t.Errorf("Message = %q", got)
	}
	if len(events.Resolved) != 1 || events.Resolved[0].TerminalState != sim.PayTerminalStateWithdrawnByBuyer {
		t.Errorf("PayWithItemResolved = %+v", events.Resolved)
	}
}

func TestWithdrawPay_AuthAndState(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCShared, huddleID: "h1", coins: 50},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1"},
	})
	defer stop()
	at := time.Now().UTC()
	seedLedgerEntry(t, w, sim.PayLedgerEntry{
		ID: 1, BuyerID: "alice", SellerID: "bob",
		ItemKind: "stew", Qty: 1, Amount: 4,
		State: sim.PayLedgerStatePending, ExpiresAt: at.Add(3 * time.Minute),
		SceneID: "sc1", HuddleID: "h1",
	})
	// Auth: seller can't withdraw.
	if _, err := w.Send(sim.WithdrawPay("bob", 1, "", at)); err == nil || !strings.Contains(err.Error(), "only the buyer") {
		t.Fatalf("want only-buyer error, got %v", err)
	}
	// State: drive to terminal then retry.
	mustSend(t, w, func(world *sim.World) {
		world.PayLedger[1].State = sim.PayLedgerStateAccepted
	})
	if _, err := w.Send(sim.WithdrawPay("alice", 1, "", at)); err == nil || !strings.Contains(err.Error(), "no longer pending") {
		t.Fatalf("want state-gate error, got %v", err)
	}
}

func TestWithdrawPay_NoCoPresenceRequired(t *testing.T) {
	// ledger-substrate § 9 — buyer can withdraw even after wandering out.
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCShared, huddleID: "h1", coins: 50},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1"},
	})
	defer stop()
	at := time.Now().UTC()
	seedLedgerEntry(t, w, sim.PayLedgerEntry{
		ID: 1, BuyerID: "alice", SellerID: "bob",
		ItemKind: "stew", Qty: 1, Amount: 4,
		State: sim.PayLedgerStatePending, ExpiresAt: at.Add(3 * time.Minute),
		SceneID: "sc1", HuddleID: "h1",
	})
	// Alice leaves the huddle entirely.
	mustSend(t, w, func(world *sim.World) {
		world.Actors["alice"].CurrentHuddleID = ""
		sim.RebuildIndicesForTest(world)
	})
	if _, err := w.Send(sim.WithdrawPay("alice", 1, "", at)); err != nil {
		t.Fatalf("WithdrawPay after leaving huddle: %v", err)
	}
}

// ============================================================
// ZBBS-WORK-391 — cross-tick duplicate-pending-offer gate +
// consume_now needs-clamp (pocket the surplus)
// ============================================================

// TestPayWithItem_SlowPath_DuplicatePendingOffer_Rejected: a new offer
// matching a still-Pending entry on (buyer, seller, item, disposition) is
// rejected at intake, naming the pending offer id. Key mirrors payOfferKey:
// price/qty differences still match; a different item or disposition is a
// different offer and passes.
func TestPayWithItem_SlowPath_DuplicatePendingOffer_Rejected(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCShared, huddleID: "h1", coins: 100},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1", inventory: map[sim.ItemKind]int{"stew": 5, "bread": 5}},
	})
	defer stop()
	at := time.Now().UTC()
	// bread, not stew: the WORK-405 eat-here clamp would flip a stew offer's
	// take-home disposition before the gate keys it, and this test is about
	// the key, not the clamp.
	seedLedgerEntry(t, w, sim.PayLedgerEntry{
		ID: 7, BuyerID: "alice", SellerID: "bob",
		ItemKind: "bread", Qty: 1, Amount: 4, ConsumeNow: false,
		State: sim.PayLedgerStatePending, ExpiresAt: at.Add(3 * time.Minute),
		SceneID: "sc1", HuddleID: "h1",
	})
	events := capturePayWithItemEvents(t, w)

	// Same (buyer, seller, item, disposition) at DIFFERENT terms — rejected.
	_, err := w.Send(sim.PayWithItem("alice", "Bob", "bread", 3, 9, false, nil, nil, 0, 0, "", at))
	if err == nil || !strings.Contains(err.Error(), "already have an offer") || !strings.Contains(err.Error(), "offer id 7") {
		t.Fatalf("want duplicate-pending rejection naming offer id 7, got %v", err)
	}
	if len(events.Offer) != 0 {
		t.Errorf("PayOfferReceived emitted for rejected duplicate: %v", events.Offer)
	}
	if ledger := readPayLedger(t, w); len(ledger) != 1 {
		t.Errorf("ledger entries = %d, want 1 (no duplicate minted)", len(ledger))
	}

	// Same item, OTHER disposition (consume_now) — a different offer, passes.
	if _, err := w.Send(sim.PayWithItem("alice", "Bob", "bread", 1, 4, true, nil, nil, 0, 0, "", at)); err != nil {
		t.Errorf("consume_now bread offer should pass the gate (different disposition): %v", err)
	}
	// Different item — passes.
	if _, err := w.Send(sim.PayWithItem("alice", "Bob", "stew", 1, 2, false, nil, nil, 0, 0, "", at)); err != nil {
		t.Errorf("stew offer should pass the gate (different item): %v", err)
	}
}

// TestPayWithItem_SlowPath_ExpiredPendingOffer_NotBlocking: a Pending entry
// past its ExpiresAt no longer blocks a fresh offer — the buyer isn't held
// hostage to the sweep's cadence.
func TestPayWithItem_SlowPath_ExpiredPendingOffer_NotBlocking(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCShared, huddleID: "h1", coins: 100},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1", inventory: map[sim.ItemKind]int{"stew": 5}},
	})
	defer stop()
	at := time.Now().UTC()
	seedLedgerEntry(t, w, sim.PayLedgerEntry{
		ID: 7, BuyerID: "alice", SellerID: "bob",
		ItemKind: "stew", Qty: 1, Amount: 4, ConsumeNow: false,
		State: sim.PayLedgerStatePending, ExpiresAt: at.Add(-time.Minute),
		SceneID: "sc1", HuddleID: "h1",
	})
	if _, err := w.Send(sim.PayWithItem("alice", "Bob", "stew", 1, 4, false, nil, nil, 0, 0, "", at)); err != nil {
		t.Errorf("offer against an expired pending entry should mint: %v", err)
	}
}

// TestPayWithItem_InResponseTo_ExemptFromPendingGate: a counter-response
// (in_response_to) is a distinct lifecycle move and bypasses the duplicate-
// pending gate even when an unrelated Pending entry matches its key.
func TestPayWithItem_InResponseTo_ExemptFromPendingGate(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCShared, huddleID: "h1", coins: 100},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1", inventory: map[sim.ItemKind]int{"stew": 5}},
	})
	defer stop()
	at := time.Now().UTC()
	// The countered parent the response answers.
	seedLedgerEntry(t, w, sim.PayLedgerEntry{
		ID: 42, BuyerID: "alice", SellerID: "bob",
		ItemKind: "stew", Qty: 1, Amount: 4, ConsumeNow: false,
		State: sim.PayLedgerStateCountered, CounterAmount: 6,
		CreatedAt: at.Add(-2 * time.Minute), ResolvedAt: at.Add(-time.Minute),
		SceneID: "sc1", HuddleID: "h1",
	})
	// An unrelated Pending entry that matches the response's key.
	seedLedgerEntry(t, w, sim.PayLedgerEntry{
		ID: 43, BuyerID: "alice", SellerID: "bob",
		ItemKind: "stew", Qty: 1, Amount: 5, ConsumeNow: false,
		State: sim.PayLedgerStatePending, ExpiresAt: at.Add(3 * time.Minute),
		SceneID: "sc1", HuddleID: "h1",
	})
	if _, err := w.Send(sim.PayWithItem("alice", "Bob", "stew", 1, 6, false, nil, nil, 0, 42, "", at)); err != nil {
		t.Errorf("counter-response should bypass the pending gate: %v", err)
	}
}

// TestAcceptPay_ConsumeNow_ClampsToNeed_PocketsSurplus: accepting a
// consume_now bundle eats only what the consumer's needs absorb and pockets
// the surplus into the BUYER's inventory. The purchase itself is untouched:
// full price paid, full qty leaves the seller. This is the Prudence fix —
// a 10-unit seller-pitched bundle against a small hunger no longer burns
// nine units into a zeroed need.
func TestAcceptPay_ConsumeNow_ClampsToNeed_PocketsSurplus(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCShared, huddleID: "h1", coins: 50},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1", inventory: map[sim.ItemKind]int{"stew": 10}},
	})
	defer stop()
	at := time.Now().UTC()
	// Hunger 6 against stew (Immediate=4): absorbs ceil(6/4)=2 units.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["alice"].Needs = map[sim.NeedKey]int{"hunger": 6}
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed needs: %v", err)
	}
	seedLedgerEntry(t, w, sim.PayLedgerEntry{
		ID: 1, BuyerID: "alice", SellerID: "bob",
		ItemKind: "stew", Qty: 10, Amount: 20, ConsumeNow: true,
		State: sim.PayLedgerStatePending, ExpiresAt: at.Add(3 * time.Minute),
		SceneID: "sc1", HuddleID: "h1",
	})
	events := capturePayWithItemEvents(t, w)
	if _, err := w.Send(sim.AcceptPay("bob", 1, at)); err != nil {
		t.Fatalf("AcceptPay: %v", err)
	}

	if len(events.Consumed) != 1 {
		t.Fatalf("ItemConsumed = %d, want 1", len(events.Consumed))
	}
	evt := events.Consumed[0]
	if evt.Qty != 2 || evt.Kept != 8 {
		t.Errorf("event Qty/Kept = %d/%d, want 2/8", evt.Qty, evt.Kept)
	}
	if got := evt.Applied["hunger"]; got != 6 {
		t.Errorf("Applied[hunger] = %d, want 6", got)
	}

	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		alice := world.Actors["alice"]
		bob := world.Actors["bob"]
		return []int{alice.Inventory["stew"], alice.Needs["hunger"], alice.Coins, bob.Inventory["stew"], bob.Coins}, nil
	}})
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	got := res.([]int)
	if got[0] != 8 {
		t.Errorf("alice pocketed stew = %d, want 8", got[0])
	}
	if got[1] != 0 {
		t.Errorf("alice hunger = %d, want 0", got[1])
	}
	if got[2] != 30 {
		t.Errorf("alice coins = %d, want 30 (paid the full 20)", got[2])
	}
	if got[3] != 0 {
		t.Errorf("bob stew = %d, want 0 (full qty left the seller)", got[3])
	}
	if got[4] != 20 {
		t.Errorf("bob coins = %d, want 20 (received the full amount)", got[4])
	}

	// LLM-188: the pocketed surplus is recorded on the ledger entry so the
	// buyer's settled-offer perception line can reconcile eaten-vs-kept.
	if e, ok := readPayLedger(t, w)[1]; !ok || e.KeptUnits != 8 {
		t.Errorf("ledger entry KeptUnits = %d (present=%v), want 8", e.KeptUnits, ok)
	}
}

// TestPayWithItem_SlowPath_ZeroExpiryPendingOffer_StillBlocks: a Pending
// entry with a zero ExpiresAt (legacy/seeded shape) is never-expiring, not
// always-expired — the gate must still reject the duplicate.
func TestPayWithItem_SlowPath_ZeroExpiryPendingOffer_StillBlocks(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCShared, huddleID: "h1", coins: 100},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1", inventory: map[sim.ItemKind]int{"stew": 5}},
	})
	defer stop()
	at := time.Now().UTC()
	// bread for the same reason as the duplicate-gate test above: keep the
	// WORK-405 clamp out of a test about expiry semantics.
	seedLedgerEntry(t, w, sim.PayLedgerEntry{
		ID: 7, BuyerID: "alice", SellerID: "bob",
		ItemKind: "bread", Qty: 1, Amount: 4, ConsumeNow: false,
		State:   sim.PayLedgerStatePending, // ExpiresAt deliberately zero
		SceneID: "sc1", HuddleID: "h1",
	})
	_, err := w.Send(sim.PayWithItem("alice", "Bob", "bread", 1, 4, false, nil, nil, 0, 0, "", at))
	if err == nil || !strings.Contains(err.Error(), "offer id 7") {
		t.Fatalf("zero-ExpiresAt pending entry should still block, got %v", err)
	}
}

// ---- ZBBS-HOME-424: opportunistic quote auto-match ---------------------

// TestPayWithItem_AutoMatch_BareOfferTakesOpenQuote — a bare pay_with_item
// (no quote_id) whose terms match the seller's open quote settles via the
// fast path instead of minting the quote's mirror image as a crossing
// pending offer (the hud-6c849d… buyer/seller deadlock).
func TestPayWithItem_AutoMatch_BareOfferTakesOpenQuote(t *testing.T) {
	w, stop, at := buildFastPathFixture(t, 7)
	defer stop()
	// bread quote, not the fixture's stew one: the WORK-405 eat-here clamp
	// would flip the bare offer's take-home disposition and the auto-match
	// term predicates require disposition equality with the quote.
	mustSend(t, w, func(world *sim.World) {
		world.Actors["bob"].Inventory["bread"] = 5
	})
	seedQuote(t, w, sim.SceneQuote{
		ID: 8, SceneID: "sc1", SellerID: "bob",
		Lines: []sim.QuoteLine{{ItemKind: "bread", Qty: 1}}, Amount: 4, State: sim.SceneQuoteStateActive,
		CreatedAt: at, ExpiresAt: at.Add(10 * time.Minute),
	})

	res, err := w.Send(sim.PayWithItem("alice", "Bob", "bread", 1, 4, false, nil, nil, 0, 0, "", at))
	if err != nil {
		t.Fatalf("PayWithItem bare offer: %v", err)
	}
	result := res.(sim.PayWithItemResult)
	if !result.FastPath {
		t.Error("FastPath = false — a bare offer matching an open quote should take the quote")
	}
	if result.State != sim.PayLedgerStateAccepted {
		t.Errorf("State = %q, want accepted", result.State)
	}
}

// TestPayWithItem_AutoMatch_WithdrawsCrossingOffer — when the auto-match
// settles, the buyer's own older pending offer MIRRORING the settled terms
// (same scene/kind/qty/disposition/consumers) resolves WithdrawnByBuyer
// instead of staying pending (a stale mirror the seller could later accept
// → double settle), while a DISTINCT live order for the same goods at
// different terms survives untouched (code_review). The pre-seeded crossing
// entry also proves ordering: the auto-match runs BEFORE the cross-tick
// duplicate gate, which would otherwise reject this very call.
func TestPayWithItem_AutoMatch_WithdrawsCrossingOffer(t *testing.T) {
	w, stop, at := buildFastPathFixture(t, 7)
	defer stop()
	// bread throughout, for the same WORK-405 reason as the auto-match test
	// above — this test pins crossing-offer hygiene, not the clamp.
	mustSend(t, w, func(world *sim.World) {
		world.Actors["bob"].Inventory["bread"] = 5
	})
	seedQuote(t, w, sim.SceneQuote{
		ID: 8, SceneID: "sc1", SellerID: "bob",
		Lines: []sim.QuoteLine{{ItemKind: "bread", Qty: 1}}, Amount: 4, State: sim.SceneQuoteStateActive,
		CreatedAt: at, ExpiresAt: at.Add(10 * time.Minute),
	})
	// The mirror: same scene, kind, qty, disposition, consumers as the take.
	seedLedgerEntry(t, w, sim.PayLedgerEntry{
		ID: 55, BuyerID: "alice", SellerID: "bob",
		ItemKind: "bread", Qty: 1, Amount: 4, ConsumeNow: false,
		State: sim.PayLedgerStatePending, CreatedAt: at.Add(-time.Minute),
		ExpiresAt: at.Add(10 * time.Minute), SceneID: "sc1", HuddleID: "h1",
	})
	// A distinct live order: same goods, different qty + disposition — the
	// 10-water-take-home case. Must NOT be withdrawn by the 1-bread take.
	seedLedgerEntry(t, w, sim.PayLedgerEntry{
		ID: 56, BuyerID: "alice", SellerID: "bob",
		ItemKind: "bread", Qty: 3, Amount: 12, ConsumeNow: true,
		State: sim.PayLedgerStatePending, CreatedAt: at.Add(-time.Minute),
		ExpiresAt: at.Add(10 * time.Minute), SceneID: "sc1", HuddleID: "h1",
	})

	res, err := w.Send(sim.PayWithItem("alice", "Bob", "bread", 1, 4, false, nil, nil, 0, 0, "", at))
	if err != nil {
		t.Fatalf("PayWithItem bare offer with crossing entry: %v", err)
	}
	if !res.(sim.PayWithItemResult).FastPath {
		t.Error("FastPath = false — the crossing entry must not block the quote take")
	}

	ledger := readPayLedger(t, w)
	if got := ledger[55].State; got != sim.PayLedgerStateWithdrawnByBuyer {
		t.Errorf("mirror offer state = %q, want withdrawn_by_buyer", got)
	}
	if got := ledger[56].State; got != sim.PayLedgerStatePending {
		t.Errorf("distinct-terms offer state = %q, want pending (must survive the sweep)", got)
	}
}

// TestPayWithItem_AutoMatch_BelowFloorStaysSlowPath — an under-floor amount
// is a haggle, not a take: it stakes a normal pending offer for the seller
// to accept/decline/counter, exactly as before the auto-match existed.
func TestPayWithItem_AutoMatch_BelowFloorStaysSlowPath(t *testing.T) {
	w, stop, at := buildFastPathFixture(t, 7)
	defer stop()

	res, err := w.Send(sim.PayWithItem("alice", "Bob", "stew", 1, 3, false, nil, nil, 0, 0, "", at))
	if err != nil {
		t.Fatalf("PayWithItem below-floor offer: %v", err)
	}
	result := res.(sim.PayWithItemResult)
	if result.FastPath {
		t.Error("FastPath = true — a below-floor offer must not auto-take the quote")
	}
	if result.State != sim.PayLedgerStatePending {
		t.Errorf("State = %q, want pending", result.State)
	}
}

// TestPayWithItem_AutoMatch_TargetedElsewhereStaysSlowPath — a quote
// targeted at a different buyer is not takeable by this one; the bare offer
// stakes normally.
func TestPayWithItem_AutoMatch_TargetedElsewhereStaysSlowPath(t *testing.T) {
	w, stop, at := buildFastPathFixture(t, 7)
	defer stop()
	mustSend(t, w, func(world *sim.World) {
		world.Quotes[7].TargetBuyer = "carol"
	})

	res, err := w.Send(sim.PayWithItem("alice", "Bob", "stew", 1, 4, false, nil, nil, 0, 0, "", at))
	if err != nil {
		t.Fatalf("PayWithItem with elsewhere-targeted quote: %v", err)
	}
	result := res.(sim.PayWithItemResult)
	if result.FastPath {
		t.Error("FastPath = true — a quote targeted at another buyer must not auto-match")
	}
	if result.State != sim.PayLedgerStatePending {
		t.Errorf("State = %q, want pending", result.State)
	}
}

// TestPayWithItem_AutoMatch_BarterExempt — an offer paying (partly) in goods
// can't take a quote (a quote is a coin price — ZBBS-HOME-393); it stakes a
// normal barter offer even when an otherwise-matching quote is open.
func TestPayWithItem_AutoMatch_BarterExempt(t *testing.T) {
	w, stop, at := buildFastPathFixture(t, 7)
	defer stop()
	mustSend(t, w, func(world *sim.World) {
		world.Actors["alice"].Inventory = map[sim.ItemKind]int{"bread": 2}
	})

	res, err := w.Send(sim.PayWithItem("alice", "Bob", "stew", 1, 4, false, nil,
		[]sim.PayItemInput{{Item: "bread", Qty: 1}}, 0, 0, "", at))
	if err != nil {
		t.Fatalf("PayWithItem barter offer: %v", err)
	}
	result := res.(sim.PayWithItemResult)
	if result.FastPath {
		t.Error("FastPath = true — a barter offer must not auto-take a coin quote")
	}
	if result.State != sim.PayLedgerStatePending {
		t.Errorf("State = %q, want pending", result.State)
	}
}

// TestPayWithItem_PayItems_ServiceRejected (ZBBS-HOME-424) — a service item
// is not a transferable good: offering one as payment is rejected at intake,
// in both the buyer's pay_items and (via the shared resolver) a seller's
// counter goods.
func TestPayWithItem_PayItems_ServiceRejected(t *testing.T) {
	w, stop, at := buildFastPathFixture(t, 7)
	defer stop()
	mustSend(t, w, func(world *sim.World) {
		world.ItemKinds["nights_stay"] = &sim.ItemKindDef{
			Name: "nights_stay", DisplayLabel: "a night's stay",
			Capabilities: []string{"service", "lodging"},
		}
		world.Actors["alice"].Inventory = map[sim.ItemKind]int{"nights_stay": 1}
	})

	_, err := w.Send(sim.PayWithItem("alice", "Bob", "stew", 1, 0, false, nil,
		[]sim.PayItemInput{{Item: "nights_stay", Qty: 1}}, 0, 0, "", at))
	if err == nil || !strings.Contains(err.Error(), "service") {
		t.Fatalf("want service-payment rejection, got %v", err)
	}
}

// TestPayWithItem_FastPath_BuyerDispositionWins (ZBBS-WORK-402): the quote
// is takeaway, the buyer takes it eat-here — the take settles on the fast
// path with the BUYER's disposition (ItemConsumed fires, the entry carries
// the buyer's term) instead of rejecting on a consume_now mismatch.
func TestPayWithItem_FastPath_BuyerDispositionWins(t *testing.T) {
	w, stop, at := buildFastPathFixture(t, 7)
	defer stop()
	events := capturePayWithItemEvents(t, w)
	res, err := w.Send(sim.PayWithItem("alice", "Bob", "stew", 1, 4, true, nil, nil, 7, 0, "", at))
	if err != nil {
		t.Fatalf("PayWithItem buyer-disposition take: %v", err)
	}
	r := res.(sim.PayWithItemResult)
	if !r.FastPath {
		t.Error("FastPath = false, want true (disposition no longer gates the match)")
	}
	if len(events.Consumed) != 1 || events.Consumed[0].ActorID != "alice" {
		t.Fatalf("ItemConsumed = %+v, want one consume by alice (buyer chose eat-here)", events.Consumed)
	}
	if entry := readPayLedger(t, w)[r.LedgerID]; !entry.ConsumeNow {
		t.Error("ledger ConsumeNow = false, want true (buyer's term rides the entry)")
	}
}

// TestPayWithItem_FastPath_ServiceClampsDisposition (ZBBS-WORK-402): a
// service kind has no eat-here/take-home choice — whatever the buyer sends,
// the engine forces the service shape (consume_now=false) rather than
// rejecting. Uses the production service shape (service+lodging, like
// nights_stay): the clamp keeps a confused consume_now=true take OFF the
// eat-on-the-spot branch and on the lodging Order branch — the room grant
// happens at the keeper's deliver_order, not at accept, so no bedroom
// machinery is needed here.
func TestPayWithItem_FastPath_ServiceClampsDisposition(t *testing.T) {
	w, stop, at := buildFastPathFixture(t, 7)
	defer stop()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.ItemKinds["nights_stay"] = &sim.ItemKindDef{
			Name: "nights_stay", DisplayLabel: "a night's stay",
			Capabilities: []string{"service", "lodging"},
		}
		world.Structures["inn"] = &sim.Structure{ID: "inn", DisplayName: "The Inn", Rooms: []*sim.Room{
			{ID: 1, StructureID: "inn", Kind: sim.RoomKindPrivate, Name: "bedroom_1"},
		}}
		world.Actors["bob"].WorkStructureID = "inn"
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed service kind: %v", err)
	}
	seedQuote(t, w, sim.SceneQuote{
		ID: 8, SceneID: "sc1", SellerID: "bob",
		Lines: []sim.QuoteLine{{ItemKind: "nights_stay", Qty: 1}}, Amount: 2, State: sim.SceneQuoteStateActive,
		CreatedAt: at, ExpiresAt: at.Add(10 * time.Minute),
	})
	events := capturePayWithItemEvents(t, w)
	res, err := w.Send(sim.PayWithItem("alice", "Bob", "nights_stay", 1, 2, true, nil, nil, 8, 0, "", at))
	if err != nil {
		t.Fatalf("PayWithItem service take: %v", err)
	}
	r := res.(sim.PayWithItemResult)
	if !r.FastPath {
		t.Error("FastPath = false, want true")
	}
	if entry := readPayLedger(t, w)[r.LedgerID]; entry.ConsumeNow {
		t.Error("ledger ConsumeNow = true, want false (service shape forced)")
	}
	if len(events.Consumed) != 0 {
		t.Errorf("ItemConsumed on a service take: %+v", events.Consumed)
	}
}

// TestPayWithItem_PCEatHereClampServerSide (ZBBS-WORK-403): a PC buying a
// non-portable consumable settles eat-here no matter what the request said
// — clamped on the world goroutine, so a failed catalog fetch or a direct
// API call can't carry stew out.
func TestPayWithItem_PCEatHereClampServerSide(t *testing.T) {
	w, stop, at := buildFastPathFixture(t, 7)
	defer stop()
	// Make stew a non-portable consumable and the buyer a PC.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.ItemKinds["stew"] = &sim.ItemKindDef{
			Name: "stew", DisplayLabel: "a bowl of stew",
			Satisfies: []sim.ItemSatisfaction{{Attribute: "hunger"}},
		}
		world.Actors["alice"].Kind = sim.KindPC
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// The quote proposes takeaway and the PC requests takeaway — the clamp
	// still forces eat-here.
	res, err := w.Send(sim.PayWithItem("alice", "Bob", "stew", 1, 4, false, nil, nil, 7, 0, "", at))
	if err != nil {
		t.Fatalf("PayWithItem PC take: %v", err)
	}
	r := res.(sim.PayWithItemResult)
	if entry := readPayLedger(t, w)[r.LedgerID]; !entry.ConsumeNow {
		t.Error("PC ledger ConsumeNow = false, want true (server-side eat-here clamp)")
	}
	if !r.EatHereClamped {
		t.Error("PC result EatHereClamped = false, want true")
	}
}

// TestPayWithItem_NPCBuyerEatHereClamped (ZBBS-WORK-405): same item shape,
// NPC buyer — the clamp applies to every buyer kind. v1 gated take-home of
// non-portables for all actors; no valid NPC flow buys non-portables
// take-home (such a purchase is a config bug, not a disposition to
// preserve). Until WORK-405 this test asserted the inverse (NPC exempt).
func TestPayWithItem_NPCBuyerEatHereClamped(t *testing.T) {
	w, stop, at := buildFastPathFixture(t, 7)
	defer stop()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.ItemKinds["stew"] = &sim.ItemKindDef{
			Name: "stew", DisplayLabel: "a bowl of stew",
			Satisfies: []sim.ItemSatisfaction{{Attribute: "hunger"}},
		}
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	res, err := w.Send(sim.PayWithItem("alice", "Bob", "stew", 1, 4, false, nil, nil, 7, 0, "", at))
	if err != nil {
		t.Fatalf("PayWithItem NPC take: %v", err)
	}
	r := res.(sim.PayWithItemResult)
	if entry := readPayLedger(t, w)[r.LedgerID]; !entry.ConsumeNow {
		t.Error("NPC ledger ConsumeNow = false, want true (clamp applies to all buyers, WORK-405)")
	}
	if !r.EatHereClamped {
		t.Error("NPC result EatHereClamped = false, want true")
	}
}
