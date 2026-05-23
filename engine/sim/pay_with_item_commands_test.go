package sim_test

import (
	"context"
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
			ID:               s.id,
			DisplayName:      s.displayName,
			Kind:             s.kind,
			State:            sim.StateIdle,
			StateEnteredAt:   now,
			Coins:            s.coins,
			Inventory:        s.inventory,
			CurrentHuddleID:  s.huddleID,
			BreakUntil:       s.breakUntil,
			RecentActions:    sim.NewRingBuffer[sim.Action](4),
			RecentStateTrans: sim.NewRingBuffer[sim.StateTransition](4),
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

	res, err := w.Send(sim.PayWithItem("alice", "Bob", "stew", 1, 4, false, nil, 0, 0, "", at))
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

	_, err := w.Send(sim.PayWithItem("alice", "Bob", "stew", 1, 4, false, nil, 0, 0, "", time.Now().UTC()))
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

func TestPayWithItem_SlowPath_NumericGates(t *testing.T) {
	cases := []struct {
		name   string
		amount int
		qty    int
		want   string
	}{
		{"zero_amount", 0, 1, "at least 1"},
		{"negative_amount", -5, 1, "at least 1"},
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
			_, err := w.Send(sim.PayWithItem("alice", "Bob", "stew", tc.qty, tc.amount, false, nil, 0, 0, "", time.Now().UTC()))
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
	_, err := w.Send(sim.PayWithItem("alice", "Bob", "stew", 1, 4, false, nil, 0, 0, "", time.Now().UTC()))
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
	_, err := w.Send(sim.PayWithItem("alice", "Bob", "stew", 1, 4, false, nil, 0, 0, "", time.Now().UTC()))
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
	_, err := w.Send(sim.PayWithItem("alice", "Bob", "stew", 1, 4, false, nil, 0, 0, "", time.Now().UTC()))
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
	_, err := w.Send(sim.PayWithItem("alice", "Alice", "stew", 1, 4, false, nil, 0, 0, "", time.Now().UTC()))
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
	_, err := w.Send(sim.PayWithItem("alice", "Bob", "stew", 1, 4, false, nil, 0, 0, "", time.Now().UTC()))
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
	_, err := w.Send(sim.PayWithItem("alice", "Bob", "fizzbuzz", 1, 4, false, nil, 0, 0, "", time.Now().UTC()))
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
	_, err := w.Send(sim.PayWithItem("alice", "Bob", "stew", 1, 4, false, too, 0, 0, "", time.Now().UTC()))
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
			_, err := w.Send(sim.PayWithItem("alice", "Bob", "stew", 1, 4, false, tc.consumers, 0, 0, "", time.Now().UTC()))
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
	res, err := w.Send(sim.PayWithItem("alice", "Bob", "stew", 1, 6, false, []string{"Alice", "Carol"}, 0, 0, "", time.Now().UTC()))
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

	res, err := w.Send(sim.PayWithItem("alice", "Bob", "stew", 1, 6, false, nil, 0, 42, "", at))
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
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
				{id: "alice", displayName: "Alice", kind: sim.KindNPCShared, huddleID: "h1", coins: 100},
				{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1", inventory: map[sim.ItemKind]int{"stew": 5}},
			})
			defer stop()
			tc.setup(t, w)
			_, err := w.Send(sim.PayWithItem("alice", "Bob", "stew", 1, 5, false, nil, 0, 99, "", at))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
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
		ItemKind:  "stew",
		Qty:       1,
		Amount:    4,
		State:     sim.SceneQuoteStateActive,
		CreatedAt: at,
		ExpiresAt: at.Add(10 * time.Minute),
	})
	return w, stop, at
}

func TestPayWithItem_FastPath_HappyPath_Takeaway(t *testing.T) {
	w, stop, at := buildFastPathFixture(t, 7)
	defer stop()
	events := capturePayWithItemEvents(t, w)

	res, err := w.Send(sim.PayWithItem("alice", "Bob", "stew", 1, 4, false, nil, 7, 0, "", at))
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

	// Coin transfer landed. Post-S6: goods stay in seller's inventory
	// until deliver_order; an Order is minted at accept-time instead.
	snap := w.Published()
	if got := snap.Actors["alice"].Coins; got != 46 {
		t.Errorf("alice.Coins = %d, want 46", got)
	}
	if got := snap.Actors["bob"].Coins; got != 4 {
		t.Errorf("bob.Coins = %d, want 4", got)
	}
	if got := snap.Actors["alice"].InventoryHash; got != 0 {
		t.Errorf("alice.InventoryHash = %d, want 0 (S6: goods deferred to deliver_order)", got)
	}
	// Order minted at accept-time.
	var foundOrder *sim.Order
	for _, o := range snap.Orders {
		if o != nil && o.SellerID == "bob" && o.BuyerID == "alice" && o.State == sim.OrderStateReady {
			foundOrder = o
			break
		}
	}
	if foundOrder == nil {
		t.Fatalf("no Ready Order minted at fast-path accept; snapshot.Orders = %+v", snap.Orders)
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
	res, err := w.Send(sim.PayWithItem("alice", "Bob", "stew", 1, 4, true, nil, 7, 0, "", at))
	if err != nil {
		t.Fatalf("PayWithItem fast-path consume_now: %v", err)
	}
	if !res.(sim.PayWithItemResult).FastPath {
		t.Error("FastPath = false")
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
					world.Quotes[7].ItemKind = "ale"
				})
			},
			want: "different terms: item",
		},
		{
			name:    "qty_mismatch",
			argItem: "stew", argQty: 2, argAmount: 4,
			want: "different terms: qty",
		},
		{
			name:    "consume_now_mismatch",
			argItem: "stew", argQty: 1, argAmount: 4,
			argConsumeNow: true,
			want:          "different terms: consume_now",
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
			want: "doesn't have enough",
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
			_, err := w.Send(sim.PayWithItem("alice", "Bob", item, qty, amount, tc.argConsumeNow, tc.argConsumers, quoteID, 0, "", at))
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
	snap := w.Published()
	if got := snap.Actors["alice"].Coins; got != 46 {
		t.Errorf("alice.Coins = %d, want 46", got)
	}
	if got := snap.Actors["bob"].Coins; got != 4 {
		t.Errorf("bob.Coins = %d, want 4", got)
	}
}

// TestAcceptPay_StockReservation_PreventsOverselling is the PR S6 R1
// code_review regression test. Pre-fix: post-S6, accept no longer
// moves goods, so two pending offers against the same 1-stew
// inventory could both accept (gate-10 only saw the raw seller
// inventory, not the outstanding Order obligations). Post-fix:
// outstandingReadyOrderQty subtracts Ready-Order obligations from
// visible inventory before the stock comparison.
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

	// First accept succeeds, mints an Order (S6 takeaway path), and
	// reserves the stew.
	if _, err := w.Send(sim.AcceptPay("bob", 1, at)); err != nil {
		t.Fatalf("first AcceptPay: %v", err)
	}
	ledger := readPayLedger(t, w)
	if ledger[1].State != sim.PayLedgerStateAccepted {
		t.Errorf("ledger[1].State = %q, want accepted", ledger[1].State)
	}
	// Bob's inventory still shows 1 stew (goods stay until deliver_order).
	snap := w.Published()
	if got := snap.Actors["bob"].InventoryHash; got != 1 {
		t.Errorf("bob.InventoryHash = %d, want 1 (goods retained)", got)
	}
	// But outstandingReadyOrderQty reports 1 stew reserved.
	got, _ := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return sim.OutstandingReadyOrderQty(world, "bob", "stew"), nil
	}})
	if got.(int) != 1 {
		t.Errorf("outstandingReadyOrderQty = %d, want 1 (reservation)", got)
	}

	// Second accept must FAIL — visible inventory (1) - reserved (1)
	// = 0, less than required (1). Flip to FailedInsufficientStock.
	if _, err := w.Send(sim.AcceptPay("bob", 2, at)); err != nil {
		t.Fatalf("second AcceptPay (transitioning): %v", err)
	}
	ledger = readPayLedger(t, w)
	if ledger[2].State != sim.PayLedgerStateFailedInsufficientStock {
		t.Errorf("ledger[2].State = %q, want failed_insufficient_stock (over-selling rejected)", ledger[2].State)
	}

	// Carol got her coins back (atomic transfer didn't fire).
	snap = w.Published()
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

	if _, err := w.Send(sim.AcceptPay("bob", 7, at.Add(time.Second))); err != nil {
		t.Fatalf("AcceptPay (transitioning): %v", err)
	}
	ledger := readPayLedger(t, w)
	if ledger[7].State != sim.PayLedgerStateFailedInsufficientStock {
		t.Errorf("ledger[7].State = %q, want failed_insufficient_stock (corrupt-Order overflow must fail closed)",
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
		"insufficient_stock": {
			invalidateAfterSeed: func(t *testing.T, w *sim.World, _ time.Time) {
				mustSend(t, w, func(world *sim.World) {
					delete(world.Actors["bob"].Inventory, "stew")
				})
			},
			wantTerminal: sim.PayTerminalStateFailedInsufficientStock,
		},
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

	if _, err := w.Send(sim.CounterPay("bob", 1, 6, "how about six", at)); err != nil {
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
		{name: "zero_amount", counterAmount: 0, caller: "bob", want: "at least 1"},
		{name: "negative_amount", counterAmount: -5, caller: "bob", want: "at least 1"},
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
			_, err := w.Send(sim.CounterPay(tc.caller, 1, tc.counterAmount, "", at))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
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
