package sim_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// give_commands_test.go — LLM-138. Coverage of the one-way gift command:
// GiveItems mints a gift PayLedgerEntry; the recipient resolves it with the
// reused AcceptPay (goods move) / DeclinePay (nothing moves). Reuses the
// pay-with-item test fixture (buildPayWithItemWorld / capturePayWithItemEvents
// / readPayLedger, defined in pay_with_item_commands_test.go).

// giftActorState reads one actor's coins + a copy of its inventory live on
// the world goroutine (robust to publish timing, like readPayLedger).
type giftState struct {
	coins int
	inv   map[sim.ItemKind]int
}

func readActorState(t *testing.T, w *sim.World, id sim.ActorID) giftState {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a := world.Actors[id]
		if a == nil {
			return nil, fmt.Errorf("actor %q missing", id)
		}
		inv := make(map[sim.ItemKind]int, len(a.Inventory))
		for k, v := range a.Inventory {
			inv[k] = v
		}
		return giftState{coins: a.Coins, inv: inv}, nil
	}})
	if err != nil {
		t.Fatalf("readActorState(%q): %v", id, err)
	}
	return res.(giftState)
}

func giftWorld(t *testing.T) (*sim.World, func()) {
	return buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCShared, huddleID: "h1", coins: 5, inventory: map[sim.ItemKind]int{"stew": 3}},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1", coins: 7},
	})
}

func giftLine(item sim.ItemKind, qty int) []sim.PayItemInput {
	return []sim.PayItemInput{{Item: string(item), Qty: qty}}
}

// TestGiveItems_HappyPath_AcceptMovesGoods: a gift mints Pending with the
// gift goods on PayItems, IsGift set, no bought-item leg; AcceptPay (reused
// by accept_gift) moves the goods giver→recipient with no coin movement.
func TestGiveItems_HappyPath_AcceptMovesGoods(t *testing.T) {
	w, stop := giftWorld(t)
	defer stop()

	events := capturePayWithItemEvents(t, w)
	at := time.Now().UTC()

	res, err := w.Send(sim.GiveItems("alice", "Bob", giftLine("stew", 2), "for your hunger", at))
	if err != nil {
		t.Fatalf("GiveItems: %v", err)
	}
	result := res.(sim.PayWithItemResult)
	if result.State != sim.PayLedgerStatePending || result.FastPath || result.LedgerID == 0 {
		t.Fatalf("gift mint result = %+v", result)
	}

	if len(events.Offer) != 1 {
		t.Fatalf("PayOfferReceived events = %d, want 1 (recipient must be warranted)", len(events.Offer))
	}

	ledger := readPayLedger(t, w)
	entry := ledger[result.LedgerID]
	if !entry.IsGift {
		t.Error("entry.IsGift = false, want true")
	}
	if entry.BuyerID != "alice" || entry.SellerID != "bob" {
		t.Errorf("entry giver/recipient = %q/%q, want alice/bob", entry.BuyerID, entry.SellerID)
	}
	if entry.ItemKind != "" || entry.Qty != 0 || entry.Amount != 0 {
		t.Errorf("gift carries a bought-item/coin leg: ItemKind=%q Qty=%d Amount=%d", entry.ItemKind, entry.Qty, entry.Amount)
	}
	if len(entry.PayItems) != 1 || entry.PayItems[0].Kind != "stew" || entry.PayItems[0].Qty != 2 {
		t.Errorf("gift goods = %+v, want [{stew 2}]", entry.PayItems)
	}

	// Recipient accepts via accept_gift (the gift-disposition accept).
	if _, err := w.Send(sim.AcceptGift("bob", result.LedgerID, time.Now().UTC())); err != nil {
		t.Fatalf("AcceptGift: %v", err)
	}

	ledger = readPayLedger(t, w)
	if got := ledger[result.LedgerID].State; got != sim.PayLedgerStateAccepted {
		t.Fatalf("gift state after accept = %q, want accepted", got)
	}
	alice := readActorState(t, w, "alice")
	bob := readActorState(t, w, "bob")
	if alice.inv["stew"] != 1 {
		t.Errorf("giver stew after gift = %d, want 1 (gave 2 of 3)", alice.inv["stew"])
	}
	if bob.inv["stew"] != 2 {
		t.Errorf("recipient stew after gift = %d, want 2", bob.inv["stew"])
	}
	if alice.coins != 5 || bob.coins != 7 {
		t.Errorf("coins moved on a gift: alice=%d bob=%d, want 5/7", alice.coins, bob.coins)
	}
}

// TestGiveItems_Decline_NoMovement: DeclinePay (reused by decline_gift)
// resolves the gift with nothing moving.
func TestGiveItems_Decline_NoMovement(t *testing.T) {
	w, stop := giftWorld(t)
	defer stop()

	res, err := w.Send(sim.GiveItems("alice", "Bob", giftLine("stew", 2), "", time.Now().UTC()))
	if err != nil {
		t.Fatalf("GiveItems: %v", err)
	}
	id := res.(sim.PayWithItemResult).LedgerID

	if _, err := w.Send(sim.DeclineGift("bob", id, "I've plenty, thank ye", time.Now().UTC())); err != nil {
		t.Fatalf("DeclineGift: %v", err)
	}
	ledger := readPayLedger(t, w)
	if got := ledger[id].State; got != sim.PayLedgerStateDeclined {
		t.Fatalf("gift state after decline = %q, want declined", got)
	}
	alice := readActorState(t, w, "alice")
	bob := readActorState(t, w, "bob")
	if alice.inv["stew"] != 3 || bob.inv["stew"] != 0 {
		t.Errorf("goods moved on a declined gift: giver=%d recipient=%d, want 3/0", alice.inv["stew"], bob.inv["stew"])
	}
}

// TestGiveItems_Rejects covers the intake gates that return a tool error
// (no entry minted).
func TestGiveItems_Rejects(t *testing.T) {
	cases := []struct {
		name      string
		recipient string
		items     []sim.PayItemInput
		wantErr   string
	}{
		// Naming yourself resolves to no huddle peer (the resolver excludes the
		// caller) before the defensive self-guard — either way you can't gift
		// yourself.
		{"self", "Alice", giftLine("stew", 1), "no one named"},
		{"unknown recipient", "Nobody", giftLine("stew", 1), "no one named"},
		{"no items", "Bob", nil, "at least one item"},
		{"giver lacks goods", "Bob", giftLine("stew", 99), "don't hold"},
		// An unknown kind is discovery-minted by resolvePayItems (the same as a
		// barter pay-with leg), so it fails the holds check rather than an
		// unknown-kind reject — the giver still can't gift what they don't hold.
		{"unknown item", "Bob", giftLine("fizzbuzz", 1), "don't hold"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w, stop := giftWorld(t)
			defer stop()
			_, err := w.Send(sim.GiveItems("alice", tc.recipient, tc.items, "", time.Now().UTC()))
			if err == nil {
				t.Fatalf("GiveItems(%s): want error, got nil", tc.name)
			}
			if !containsFold(err.Error(), tc.wantErr) {
				t.Errorf("GiveItems(%s) error = %q, want substring %q", tc.name, err.Error(), tc.wantErr)
			}
		})
	}
}

// TestGiveItems_DuplicatePendingGift: a second gift to the same recipient
// while one is pending is rejected (the cross-tick duplicate gate).
func TestGiveItems_DuplicatePendingGift(t *testing.T) {
	w, stop := giftWorld(t)
	defer stop()

	if _, err := w.Send(sim.GiveItems("alice", "Bob", giftLine("stew", 1), "", time.Now().UTC())); err != nil {
		t.Fatalf("first GiveItems: %v", err)
	}
	_, err := w.Send(sim.GiveItems("alice", "Bob", giftLine("stew", 1), "", time.Now().UTC()))
	if err == nil {
		t.Fatal("second GiveItems to same recipient: want duplicate error, got nil")
	}
	if !containsFold(err.Error(), "already offered") {
		t.Errorf("duplicate-gift error = %q, want 'already offered'", err.Error())
	}
}

// TestGiveItems_AcceptRevalidatesGiverHoldings: gate 12 (reused) flips the
// gift to failed_insufficient_goods if the giver no longer holds the goods at
// accept time.
func TestGiveItems_AcceptRevalidatesGiverHoldings(t *testing.T) {
	w, stop := giftWorld(t)
	defer stop()

	res, err := w.Send(sim.GiveItems("alice", "Bob", giftLine("stew", 2), "", time.Now().UTC()))
	if err != nil {
		t.Fatalf("GiveItems: %v", err)
	}
	id := res.(sim.PayWithItemResult).LedgerID

	// Drain the giver's stew between offer and accept.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		delete(world.Actors["alice"].Inventory, "stew")
		return nil, nil
	}}); err != nil {
		t.Fatalf("drain giver: %v", err)
	}

	if _, err := w.Send(sim.AcceptGift("bob", id, time.Now().UTC())); err != nil {
		t.Fatalf("AcceptGift: %v", err)
	}
	ledger := readPayLedger(t, w)
	if got := ledger[id].State; got != sim.PayLedgerStateFailedInsufficientGoods {
		t.Errorf("gift state = %q, want failed_insufficient_goods (giver no longer holds the goods)", got)
	}
	if bob := readActorState(t, w, "bob"); bob.inv["stew"] != 0 {
		t.Errorf("recipient got goods on a failed gift accept: %d", bob.inv["stew"])
	}
}

func containsFold(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexFold(s, sub) >= 0)
}

func indexFold(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if equalFold(s[i:i+len(sub)], sub) {
			return i
		}
	}
	return -1
}

func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}

// TestGift_DispositionBoundary (LLM-138, code_review #2): accept_gift /
// decline_gift must not resolve a purchase offer, and accept_pay / decline_pay
// must not resolve a gift — the gift/buy boundary is enforced at the substrate,
// not just the gateTools advertising layer (which only governs which tools are
// shown, not which a model may dispatch).
func TestGift_DispositionBoundary(t *testing.T) {
	w, stop := giftWorld(t)
	defer stop()
	now := time.Now().UTC()
	// id 1: a normal purchase offer (alice buys stew from bob). id 2: a gift
	// (alice gives stew to bob). Both pending; bob is the resolving party.
	seedLedgerEntry(t, w, sim.PayLedgerEntry{
		ID: 1, BuyerID: "alice", SellerID: "bob", ItemKind: "stew", Qty: 1, Amount: 4,
		State: sim.PayLedgerStatePending, ExpiresAt: now.Add(time.Minute), HuddleID: "h1",
	})
	seedLedgerEntry(t, w, sim.PayLedgerEntry{
		ID: 2, BuyerID: "alice", SellerID: "bob", IsGift: true,
		PayItems: []sim.ItemKindQty{{Kind: "stew", Qty: 1}},
		State:    sim.PayLedgerStatePending, ExpiresAt: now.Add(time.Minute), HuddleID: "h1",
	})

	mustErr := func(name string, err error, want string) {
		if err == nil {
			t.Errorf("%s: want error, got nil", name)
			return
		}
		if !containsFold(err.Error(), want) {
			t.Errorf("%s: error = %q, want substring %q", name, err.Error(), want)
		}
	}
	_, e1 := w.Send(sim.AcceptGift("bob", 1, now))
	mustErr("accept_gift on purchase offer", e1, "not a gift")
	_, e2 := w.Send(sim.AcceptPay("bob", 2, now))
	mustErr("accept_pay on gift", e2, "is a gift")
	_, e3 := w.Send(sim.DeclineGift("bob", 1, "", now))
	mustErr("decline_gift on purchase offer", e3, "not a gift")
	_, e4 := w.Send(sim.DeclinePay("bob", 2, "", now))
	mustErr("decline_pay on gift", e4, "is a gift")

	// Both entries are untouched (still pending) — a verb mismatch is an
	// idempotent reject, not a transition.
	ledger := readPayLedger(t, w)
	if ledger[1].State != sim.PayLedgerStatePending || ledger[2].State != sim.PayLedgerStatePending {
		t.Errorf("verb mismatch must not transition either entry: %s / %s", ledger[1].State, ledger[2].State)
	}
}

// TestGiveItems_ForNotePersisted (LLM-138, code_review #3): the gift's optional
// "for" note rides entry.Message and reaches the gave/received_gift relationship
// facts (the accept path calls commitPayTransfer with an empty forText param, so
// the note must live on the entry).
func TestGiveItems_ForNotePersisted(t *testing.T) {
	w, stop := giftWorld(t)
	defer stop()
	res, err := w.Send(sim.GiveItems("alice", "Bob", giftLine("stew", 1), "for your hunger", time.Now().UTC()))
	if err != nil {
		t.Fatalf("GiveItems: %v", err)
	}
	id := res.(sim.PayWithItemResult).LedgerID
	if _, err := w.Send(sim.AcceptGift("bob", id, time.Now().UTC())); err != nil {
		t.Fatalf("AcceptGift: %v", err)
	}
	joined := strings.Join(readSalientFacts(t, w, "bob", "alice"), " | ") // recipient's facts about the giver
	if !strings.Contains(joined, "for your hunger") {
		t.Errorf("received-gift fact should include the for-note, got: %s", joined)
	}
	if !containsFold(joined, "gave me") {
		t.Errorf("received-gift fact should read 'gave me ...', got: %s", joined)
	}
}

// readSalientFacts returns the text of every SalientFact on `from`'s
// relationship to `to`, read live on the world goroutine.
func readSalientFacts(t *testing.T, w *sim.World, from, to sim.ActorID) []string {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a := world.Actors[from]
		if a == nil || a.Relationships == nil {
			return []string{}, nil
		}
		rel := a.Relationships[to]
		if rel == nil {
			return []string{}, nil
		}
		out := make([]string, 0, len(rel.SalientFacts))
		for _, f := range rel.SalientFacts {
			out = append(out, f.Text)
		}
		return out, nil
	}})
	if err != nil {
		t.Fatalf("readSalientFacts: %v", err)
	}
	return res.([]string)
}
