package sim_test

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// pay_lodging_steer_test.go — LLM-177. A bare `pay` never settles lodging (it
// moves coins but mints no Order and grants no RoomAccess), so a "pay 4 for a
// room" leaves the guest with nowhere to sleep — the live Ezekiel/Hannah loop,
// where the keeper even reversed coins back trying to "complete" the deal. The
// LLM-172 open-quote guard misses this because lodging phrasing ("a room for
// the night", "lodging") doesn't canonicalize to the nights_stay item kind, so
// findCoinQuoteForPay returns nil. sim.Pay now catches it — on lodging
// vocabulary (no quote posted yet) or on an open room offer between the two
// (either direction). Reuses buildFastPathFixture / buildPayWithItemWorld /
// seedQuote / seedLodgingFixture from the sibling _test files (same package).

// lodgingQuoteWorld seats a guest (alice) and a keeper (bob) in one huddle, the
// keeper's inn registered with a private bedroom, and an active public
// nights_stay quote (id 9) from the keeper in the scene.
func lodgingQuoteWorld(t *testing.T) (*sim.World, func(), time.Time) {
	t.Helper()
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCStateful, huddleID: "h1", coins: 50},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1", coins: 50},
	})
	at := time.Now().UTC()
	seedLodgingFixture(t, w, "bob", []*sim.Room{
		{ID: 2, StructureID: "inn", Kind: sim.RoomKindPrivate, Name: "bedroom_1"},
	})
	seedQuote(t, w, sim.SceneQuote{
		ID: 9, SceneID: "sc1", SellerID: "bob",
		Lines:     []sim.QuoteLine{{ItemKind: "nights_stay", Qty: 1}},
		Amount:    4,
		State:     sim.SceneQuoteStateActive,
		CreatedAt: at,
		ExpiresAt: at.Add(10 * time.Minute),
	})
	return w, stop, at
}

// TestPay_LodgingForText_NoQuote_SteersToPayWithItem — the lodger pays "for a
// room" before any quote is posted (Ezekiel's first move in the live loop). No
// open lodging quote, so the vocabulary branch fires the generic steer; coins
// stay put. The fixture's only quote is a stew quote, which must NOT match.
func TestPay_LodgingForText_NoQuote_SteersToPayWithItem(t *testing.T) {
	w, stop, at := buildFastPathFixture(t, 7)
	defer stop()

	_, err := w.Send(sim.Pay("alice", "Bob", 4, "a room for the night", at))
	if err == nil {
		t.Fatal("a bare pay for a room should be steered to pay_with_item")
	}
	msg := err.Error()
	if !strings.Contains(msg, "pay_with_item") || !strings.Contains(msg, "night's stay") {
		t.Errorf("missing the generic lodging steer: %v", err)
	}
	snap := w.Published()
	if got := snap.Actors["alice"].Coins; got != 50 {
		t.Errorf("alice.Coins = %d, want 50 (no transfer on steer)", got)
	}
	if got := snap.Actors["bob"].Coins; got != 0 {
		t.Errorf("bob.Coins = %d, want 0 (no transfer on steer)", got)
	}
}

// TestPay_LodgingForward_OpenQuote_NamesQuote — the guest pays the keeper "for a
// night" while the keeper's nights_stay quote is open. "night" alone is not a
// lodging token (so this exercises the quote branch, not the vocabulary one);
// the steer names the live quote_id so the model can redeem it.
func TestPay_LodgingForward_OpenQuote_NamesQuote(t *testing.T) {
	w, stop, at := lodgingQuoteWorld(t)
	defer stop()

	_, err := w.Send(sim.Pay("alice", "Bob", 4, "a night", at))
	if err == nil {
		t.Fatal("a bare pay toward an open room offer should be steered")
	}
	msg := err.Error()
	if !strings.Contains(msg, "quote_id 9") || !strings.Contains(msg, "pay_with_item") {
		t.Errorf("forward lodging pay should name the open quote: %v", err)
	}
	snap := w.Published()
	if snap.Actors["alice"].Coins != 50 || snap.Actors["bob"].Coins != 50 {
		t.Errorf("coins moved on a steered lodging pay: alice=%d bob=%d",
			snap.Actors["alice"].Coins, snap.Actors["bob"].Coins)
	}
}

// TestPay_LodgingReversed_KeeperPaysGuest_Steered — the reversed direction from
// the live loop: the keeper nonsensically pays the guest "for lodging". A bare
// pay is never how a room changes hands, so it is steered too; the keeper's own
// open quote is found (either-direction scan) and named.
func TestPay_LodgingReversed_KeeperPaysGuest_Steered(t *testing.T) {
	w, stop, at := lodgingQuoteWorld(t)
	defer stop()

	_, err := w.Send(sim.Pay("bob", "Alice", 4, "lodging", at))
	if err == nil {
		t.Fatal("a keeper paying a guest for lodging should be steered, not transferred")
	}
	if !strings.Contains(err.Error(), "quote_id 9") {
		t.Errorf("reversed keeper pay should still be steered with the quote: %v", err)
	}
	snap := w.Published()
	if snap.Actors["alice"].Coins != 50 || snap.Actors["bob"].Coins != 50 {
		t.Errorf("coins moved on reversed steer: alice=%d bob=%d",
			snap.Actors["alice"].Coins, snap.Actors["bob"].Coins)
	}
}

// TestPay_OpenLodgingQuote_UnrelatedTip_NotSteered — a bare pay whose text does
// not signal lodging must NOT be captured just because a PUBLIC room offer
// happens to be open in the scene (code_review). lodgingQuoteWorld's quote is
// public, and "last night's help" is neither a lodging token nor a night phrase.
func TestPay_OpenLodgingQuote_UnrelatedTip_NotSteered(t *testing.T) {
	w, stop, at := lodgingQuoteWorld(t)
	defer stop()

	if _, err := w.Send(sim.Pay("alice", "Bob", 3, "last night's help", at)); err != nil {
		t.Fatalf("an unrelated tip alongside a public room quote should proceed: %v", err)
	}
	snap := w.Published()
	if got := snap.Actors["alice"].Coins; got != 47 {
		t.Errorf("alice.Coins = %d, want 47 (tip transferred)", got)
	}
	if got := snap.Actors["bob"].Coins; got != 53 {
		t.Errorf("bob.Coins = %d, want 53 (tip received)", got)
	}
}

// TestPay_TargetedLodgingQuote_BlocksUnrelatedBarePay — a quote TARGETED at the
// counterparty marks a room deal already underway, so a bare pay between the two
// is steered even with neutral text. The quote_id is named.
func TestPay_TargetedLodgingQuote_BlocksUnrelatedBarePay(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCStateful, huddleID: "h1", coins: 50},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1", coins: 50},
	})
	defer stop()
	at := time.Now().UTC()
	seedLodgingFixture(t, w, "bob", []*sim.Room{
		{ID: 2, StructureID: "inn", Kind: sim.RoomKindPrivate, Name: "bedroom_1"},
	})
	seedQuote(t, w, sim.SceneQuote{
		ID: 11, SceneID: "sc1", SellerID: "bob", TargetBuyer: "alice",
		Lines:     []sim.QuoteLine{{ItemKind: "nights_stay", Qty: 1}},
		Amount:    4,
		State:     sim.SceneQuoteStateActive,
		CreatedAt: at,
		ExpiresAt: at.Add(10 * time.Minute),
	})

	_, err := w.Send(sim.Pay("alice", "Bob", 4, "thanks", at))
	if err == nil || !strings.Contains(err.Error(), "quote_id 11") {
		t.Fatalf("a targeted room quote should steer any bare pay between the two: %v", err)
	}
	snap := w.Published()
	if snap.Actors["alice"].Coins != 50 || snap.Actors["bob"].Coins != 50 {
		t.Errorf("coins moved on a steered targeted-quote pay: alice=%d bob=%d",
			snap.Actors["alice"].Coins, snap.Actors["bob"].Coins)
	}
}

// TestPay_NightWordTip_NotSteered — "night" is deliberately NOT a lodging token
// (a tip "for last night's help" is legitimate) and no room offer is open, so a
// payment mentioning it proceeds as a plain transfer. Guards against the
// vocabulary list over-firing.
func TestPay_NightWordTip_NotSteered(t *testing.T) {
	w, stop, at := buildFastPathFixture(t, 7)
	defer stop()

	if _, err := w.Send(sim.Pay("alice", "Bob", 3, "last night's help", at)); err != nil {
		t.Fatalf("a tip mentioning 'night' with no room offer should proceed: %v", err)
	}
	snap := w.Published()
	if got := snap.Actors["alice"].Coins; got != 47 {
		t.Errorf("alice.Coins = %d, want 47 (tip transferred)", got)
	}
	if got := snap.Actors["bob"].Coins; got != 3 {
		t.Errorf("bob.Coins = %d, want 3 (tip received)", got)
	}
}
