package sim_test

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// pay_with_item_quote_take_terms_test.go — LLM-905: a quote take names the
// deal by quote_id alone. The quote cue tells the buyer "call pay_with_item
// with quote_id N and amount M" (bundle) or adds only item and qty (one good),
// so PayWithItem fills seller / item / qty from the quote. Live trigger: Caleb
// Wendell's eight takes of Elizabeth Ellis's flour-and-cheese bundle were all
// rejected for a missing seller or item, and no coins or goods moved.

func buildQuoteTakeBundleWorld(t *testing.T) (*sim.World, func(), time.Time) {
	t.Helper()
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "caleb", displayName: "Caleb Wendell", kind: sim.KindNPCShared, huddleID: "h1", coins: 50},
		{id: "eliz", displayName: "Elizabeth Ellis", kind: sim.KindNPCShared, huddleID: "h1",
			inventory: map[sim.ItemKind]int{"blueberries": 5, "raspberries": 5}},
	})
	addBundleCatalogKinds(t, w, "blueberries", "raspberries")
	at := time.Now().UTC()
	seedQuote(t, w, sim.SceneQuote{
		ID:          61,
		SceneID:     "sc1",
		SellerID:    "eliz",
		TargetBuyer: "caleb",
		Lines:       []sim.QuoteLine{{ItemKind: "blueberries", Qty: 1}, {ItemKind: "raspberries", Qty: 2}},
		Amount:      12,
		State:       sim.SceneQuoteStateActive,
		ExpiresAt:   at.Add(10 * time.Minute),
	})
	return w, stop, at
}

// The bundle cue's exact call: no seller, no item, no qty.
func TestPayWithItem_QuoteTake_BundleNeedsOnlyQuoteIDAndAmount(t *testing.T) {
	w, stop, at := buildQuoteTakeBundleWorld(t)
	defer stop()

	res, err := w.Send(sim.PayWithItem("caleb", "", "", 0, 12, false, nil, nil, 61, 0, "", at))
	if err != nil {
		t.Fatalf("PayWithItem (quote_id + amount only): %v", err)
	}
	out := res.(sim.PayWithItemResult)
	if !out.FastPath || out.State != sim.PayLedgerStateAccepted {
		t.Fatalf("result = %+v, want fast-path accepted", out)
	}
	if out.SellerName != "Elizabeth Ellis" {
		t.Errorf("SellerName = %q, want the quote's seller", out.SellerName)
	}
	if out.ItemKind != "blueberries" || out.Qty != 1 {
		t.Errorf("ItemKind/Qty = %q/%d, want the first line blueberries/1", out.ItemKind, out.Qty)
	}
	if len(out.Lines) != 2 {
		t.Errorf("Lines = %d, want the whole bundle (2)", len(out.Lines))
	}
	buyer := readBundleActorState(t, w, "caleb")
	if buyer.inv["blueberries"] != 1 || buyer.inv["raspberries"] != 2 {
		t.Errorf("buyer inventory = %v, want blueberries:1 raspberries:2", buyer.inv)
	}
	if buyer.coins != 38 {
		t.Errorf("buyer coins = %d, want 38", buyer.coins)
	}
}

// Josiah's live workaround shape: item "bundle". The fast path ignores a
// bundle buyer's item, so a name that is not a real kind must not sink the take.
func TestPayWithItem_QuoteTake_BundleIgnoresTheNamedItem(t *testing.T) {
	w, stop, at := buildQuoteTakeBundleWorld(t)
	defer stop()

	res, err := w.Send(sim.PayWithItem("caleb", "Elizabeth Ellis", "bundle", 1, 12, false, nil, nil, 61, 0, "", at))
	if err != nil {
		t.Fatalf("PayWithItem (item \"bundle\" on a bundle quote): %v", err)
	}
	if out := res.(sim.PayWithItemResult); !out.FastPath || len(out.Lines) != 2 {
		t.Fatalf("result = %+v, want the whole bundle taken", out)
	}
}

func TestPayWithItem_QuoteTake_SingleGoodFillsSellerAndTerms(t *testing.T) {
	w, stop, at := buildFastPathFixture(t, 7) // bob quotes 1 stew for 4
	defer stop()

	res, err := w.Send(sim.PayWithItem("alice", "", "", 0, 4, true, nil, nil, 7, 0, "", at))
	if err != nil {
		t.Fatalf("PayWithItem (single-good quote, terms left out): %v", err)
	}
	out := res.(sim.PayWithItemResult)
	if !out.FastPath || out.State != sim.PayLedgerStateAccepted {
		t.Fatalf("result = %+v, want fast-path accepted", out)
	}
	if out.SellerName != "Bob" || out.ItemKind != "stew" || out.Qty != 1 {
		t.Errorf("terms = %q / %q / %d, want Bob / stew / 1", out.SellerName, out.ItemKind, out.Qty)
	}
}

// The single-good cue names item and qty but not the seller.
func TestPayWithItem_QuoteTake_SingleGoodWithoutSeller(t *testing.T) {
	w, stop, at := buildFastPathFixture(t, 7)
	defer stop()

	if _, err := w.Send(sim.PayWithItem("alice", "", "stew", 1, 4, true, nil, nil, 7, 0, "", at)); err != nil {
		t.Fatalf("PayWithItem (quote_id, item, qty, amount): %v", err)
	}
}

// A named item on a one-good quote keeps the LLM-172 exact-match reject.
func TestPayWithItem_QuoteTake_SingleGoodWrongItemStillRejects(t *testing.T) {
	w, stop, at := buildFastPathFixture(t, 7)
	defer stop()

	_, err := w.Send(sim.PayWithItem("alice", "", "ale", 1, 4, true, nil, nil, 7, 0, "", at))
	if err == nil || !strings.Contains(err.Error(), `quote 7 is for "stew"`) {
		t.Fatalf("err = %v, want the LLM-172 reject naming the quoted item", err)
	}
}

// A named seller is kept, so a take aimed at the wrong person still fails.
func TestPayWithItem_QuoteTake_NamedSellerStillChecked(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCShared, huddleID: "h1", coins: 50},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1", inventory: map[sim.ItemKind]int{"stew": 5}},
		{id: "carol", displayName: "Carol", kind: sim.KindNPCShared, huddleID: "h1", inventory: map[sim.ItemKind]int{"stew": 5}},
	})
	defer stop()
	at := time.Now().UTC()
	seedQuote(t, w, sim.SceneQuote{
		ID: 7, SceneID: "sc1", SellerID: "bob",
		Lines: []sim.QuoteLine{{ItemKind: "stew", Qty: 1}}, Amount: 4, State: sim.SceneQuoteStateActive,
		CreatedAt: at, ExpiresAt: at.Add(10 * time.Minute),
	})

	_, err := w.Send(sim.PayWithItem("alice", "Carol", "", 0, 4, true, nil, nil, 7, 0, "", at))
	if err == nil || !strings.Contains(err.Error(), "different seller") {
		t.Fatalf("err = %v, want the different-seller reject", err)
	}
}

func TestPayWithItem_QuoteTake_UnknownQuoteWithoutSeller(t *testing.T) {
	w, stop, at := buildFastPathFixture(t, 7)
	defer stop()

	_, err := w.Send(sim.PayWithItem("alice", "", "", 0, 4, true, nil, nil, 99, 0, "", at))
	if err == nil || !strings.Contains(err.Error(), "quote 99 not available") {
		t.Fatalf("err = %v, want 'quote 99 not available'", err)
	}
}

// The exported command bounds-checks the caller's qty before a bundle replaces
// it (code_review): an out-of-range qty rejects and nothing moves.
func TestPayWithItem_QuoteTake_BundleRejectsOutOfRangeQty(t *testing.T) {
	for _, qty := range []int{-1, sim.MaxPayWithItemQty + 1} {
		w, stop, at := buildQuoteTakeBundleWorld(t)
		_, err := w.Send(sim.PayWithItem("caleb", "", "", qty, 12, false, nil, nil, 61, 0, "", at))
		if err == nil || !strings.Contains(err.Error(), "qty out of range") {
			t.Errorf("qty %d: err = %v, want a qty range reject", qty, err)
		}
		buyer := readBundleActorState(t, w, "caleb")
		if buyer.coins != 50 || len(readPayLedger(t, w)) != 0 {
			t.Errorf("qty %d: coins=%d ledger=%d, want nothing moved", qty, buyer.coins, len(readPayLedger(t, w)))
		}
		stop()
	}
}

// An omitted seller resolves by the quote's actor ID, so two people sharing the
// seller's display name do not make a valid quote_id ambiguous (code_review).
func TestPayWithItem_QuoteTake_OmittedSellerResolvesByID(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCShared, huddleID: "h1", coins: 50},
		{id: "bob1", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1", inventory: map[sim.ItemKind]int{"stew": 5}},
		{id: "bob2", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1", inventory: map[sim.ItemKind]int{"stew": 5}},
	})
	defer stop()
	at := time.Now().UTC()
	seedQuote(t, w, sim.SceneQuote{
		ID: 7, SceneID: "sc1", SellerID: "bob2",
		Lines: []sim.QuoteLine{{ItemKind: "stew", Qty: 1}}, Amount: 4, State: sim.SceneQuoteStateActive,
		CreatedAt: at, ExpiresAt: at.Add(10 * time.Minute),
	})

	if _, err := w.Send(sim.PayWithItem("alice", "", "", 0, 4, true, nil, nil, 7, 0, "", at)); err != nil {
		t.Fatalf("PayWithItem (omitted seller, shared display name): %v", err)
	}
	if got := w.Published().Actors["bob2"].Coins; got != 4 {
		t.Errorf("bob2.Coins = %d, want 4 (the quote's seller is paid)", got)
	}
	if got := w.Published().Actors["bob1"].Coins; got != 0 {
		t.Errorf("bob1.Coins = %d, want 0", got)
	}
}

func TestPayWithItem_QuoteTake_OmittedSellerNotInHuddle(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCShared, huddleID: "h1", coins: 50},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "", inventory: map[sim.ItemKind]int{"stew": 5}},
	})
	defer stop()
	at := time.Now().UTC()
	seedQuote(t, w, sim.SceneQuote{
		ID: 7, SceneID: "sc1", SellerID: "bob",
		Lines: []sim.QuoteLine{{ItemKind: "stew", Qty: 1}}, Amount: 4, State: sim.SceneQuoteStateActive,
		CreatedAt: at, ExpiresAt: at.Add(10 * time.Minute),
	})

	_, err := w.Send(sim.PayWithItem("alice", "", "", 0, 4, true, nil, nil, 7, 0, "", at))
	if err == nil || !strings.Contains(err.Error(), "need to be in Bob's huddle to take quote 7") {
		t.Fatalf("err = %v, want the co-presence reject", err)
	}
}

// Line order does not change a bundle take: either order settles the whole lot.
func TestPayWithItem_QuoteTake_BundleLineOrderNeutral(t *testing.T) {
	orders := [][]sim.QuoteLine{
		{{ItemKind: "blueberries", Qty: 1}, {ItemKind: "raspberries", Qty: 2}},
		{{ItemKind: "raspberries", Qty: 2}, {ItemKind: "blueberries", Qty: 1}},
	}
	for i, lines := range orders {
		w, stop, at := buildQuoteTakeBundleWorld(t)
		mustSend(t, w, func(world *sim.World) {
			world.Quotes[61].Lines = lines
		})
		if _, err := w.Send(sim.PayWithItem("caleb", "", "", 0, 12, false, nil, nil, 61, 0, "", at)); err != nil {
			t.Errorf("order %d: %v", i, err)
		}
		buyer := readBundleActorState(t, w, "caleb")
		if buyer.inv["blueberries"] != 1 || buyer.inv["raspberries"] != 2 || buyer.coins != 38 {
			t.Errorf("order %d: buyer = %+v, want blueberries:1 raspberries:2 coins:38", i, buyer)
		}
		stop()
	}
}
