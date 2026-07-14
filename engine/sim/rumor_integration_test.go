package sim_test

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// rumor_integration_test.go — LLM-387: end-to-end coverage of the two rumor hook
// SEAMS that the unit tests around seedShortOnCoinRumor / propagateRumorOnSpeak
// can't reach, because they depend on the surrounding command flow: that a real
// short-buyer accept lands on FailedInsufficientFunds with entry.HuddleID
// populated, and that the real SpeakTo path fires propagation with LastUtteranceAtBy
// behaving as the active-conversant gate expects.

// actorRumorsForTest reads a copy of an actor's rumor known-set on the world
// goroutine (World.Actors is owned by World.Run; a direct read would race).
func actorRumorsForTest(t *testing.T, w *sim.World, id sim.ActorID) []sim.KnownRumor {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a := world.Actors[id]
		if a == nil {
			return []sim.KnownRumor(nil), nil
		}
		return append([]sim.KnownRumor(nil), a.Rumors...), nil
	}})
	if err != nil {
		t.Fatalf("read rumors for %s: %v", id, err)
	}
	return res.([]sim.KnownRumor)
}

// TestRumorSeededThroughRealInsufficientFundsAccept drives a real PayWithItem
// offer that becomes unaffordable before accept, so the real AcceptPay flow flips
// to FailedInsufficientFunds at the funds gate and fires seedShortOnCoinRumor. It
// proves the two seed-seam assumptions: FailedInsufficientFunds is the terminal a
// short buyer actually reaches, and entry.HuddleID is populated at
// finalizePayLedgerTerminal — so a CO-PRESENT witness (not just the seller) is
// seeded. An empty/stale HuddleID would silently degrade seeding to seller-only,
// which this test would catch.
func TestRumorSeededThroughRealInsufficientFundsAccept(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "ezekiel", displayName: "Ezekiel Crane", kind: sim.KindNPCShared, huddleID: "h1", coins: 10},
		{id: "hannah", displayName: "Hannah", kind: sim.KindNPCShared, huddleID: "h1", inventory: map[sim.ItemKind]int{"stew": 5}},
		{id: "john", displayName: "John Ellis", kind: sim.KindNPCShared, huddleID: "h1"},
	})
	defer stop()

	at := time.Now().UTC()
	// Ezekiel offers 6 coins for stew — affordable now (he holds 10), so the offer
	// mints a pending entry carrying HuddleID h1.
	if _, err := w.Send(sim.PayWithItem("ezekiel", "Hannah", "stew", 1, 6, false, nil, nil, 0, 0, "", at)); err != nil {
		t.Fatalf("PayWithItem: %v", err)
	}
	var ledgerID sim.LedgerID
	found := false
	for id, e := range readPayLedger(t, w) {
		if e.State == sim.PayLedgerStatePending {
			ledgerID, found = id, true
		}
	}
	if !found {
		t.Fatal("no pending ledger entry minted")
	}

	// Ezekiel spends down to 4 before Hannah accepts — now he can't cover the 6.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["ezekiel"].Coins = 4
		return nil, nil
	}}); err != nil {
		t.Fatalf("drain coins: %v", err)
	}

	if _, err := w.Send(sim.AcceptPay("hannah", ledgerID, at)); err != nil {
		t.Fatalf("AcceptPay: %v", err)
	}

	// The settlement fell through for insufficient funds (seam 1).
	if got := readPayLedger(t, w)[ledgerID].State; got != sim.PayLedgerState(sim.PayTerminalStateFailedInsufficientFunds) {
		t.Fatalf("entry state = %v, want FailedInsufficientFunds", got)
	}

	// Seller carries a first-hand, rung-0 rumor about the buyer.
	seller := actorRumorsForTest(t, w, "hannah")
	if len(seller) != 1 || seller[0].SubjectID != "ezekiel" || !seller[0].FirstHand || seller[0].Rung != 0 {
		t.Fatalf("seller rumor = %+v, want first-hand rung-0 about ezekiel", seller)
	}
	// The co-present witness was seeded too — proves entry.HuddleID flowed through
	// (seam 2).
	witness := actorRumorsForTest(t, w, "john")
	if len(witness) != 1 || witness[0].SubjectID != "ezekiel" {
		t.Fatalf("co-present witness rumor = %+v, want a rumor about ezekiel", witness)
	}
	// The subject (buyer) carries nothing about itself.
	if buyer := actorRumorsForTest(t, w, "ezekiel"); len(buyer) != 0 {
		t.Fatalf("buyer must not carry a self-rumor, got %+v", buyer)
	}
}

// TestRumorPropagatesThroughRealSpeak drives a real SpeakTo: a seller already
// carrying a rumor about an ABSENT subject speaks in a huddle, and the SpeakTo
// path fires propagateRumorOnSpeak — spreading it (escalated to hearsay) to the
// huddle's ACTIVE conversant while leaving a silent bystander untouched. Proves
// the SpeakTo hook placement + the LastUtteranceAtBy active-conversant gate behave
// as propagation expects.
func TestRumorPropagatesThroughRealSpeak(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "hannah", displayName: "Hannah", kind: sim.KindNPCShared, huddleID: "h1"},
		{id: "john", displayName: "John Ellis", kind: sim.KindNPCShared, huddleID: "h1"},
		{id: "mary", displayName: "Mary", kind: sim.KindNPCShared, huddleID: "h1"},
	})
	defer stop()

	base := time.Now().UTC()
	// Hannah already carries a first-hand rumor about Ezekiel, who is NOT in this
	// huddle (so it's shareable — not gossip to his face).
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["hannah"].Rumors = []sim.KnownRumor{{
			Topic: sim.RumorTopicShortOnCoin, SubjectID: "ezekiel", SubjectName: "Ezekiel Crane",
			Rung: 0, HeardAt: base, FirstHand: true,
		}}
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed hannah's rumor: %v", err)
	}

	// John speaks first → he becomes an active conversant. Mary stays silent.
	if _, err := w.Send(sim.SpeakTo("john", "Good morrow, Hannah.", "", nil, true, base)); err != nil {
		t.Fatalf("john speak: %v", err)
	}
	// Hannah speaks → propagateRumorOnSpeak fires.
	if _, err := w.Send(sim.SpeakTo("hannah", "And to you, John.", "", nil, true, base.Add(time.Second))); err != nil {
		t.Fatalf("hannah speak: %v", err)
	}

	// John (active) received the rumor, escalated to rung-1 hearsay.
	john := actorRumorsForTest(t, w, "john")
	if len(john) != 1 || john[0].SubjectID != "ezekiel" || john[0].Rung != 1 || john[0].FirstHand {
		t.Fatalf("active conversant rumor = %+v, want rung-1 hearsay about ezekiel", john)
	}
	// Mary (silent) received nothing.
	if mary := actorRumorsForTest(t, w, "mary"); len(mary) != 0 {
		t.Fatalf("silent bystander must not receive the rumor, got %+v", mary)
	}
	// Hannah's own rumor stays first-hand rung-0.
	hannah := actorRumorsForTest(t, w, "hannah")
	if len(hannah) != 1 || !hannah[0].FirstHand || hannah[0].Rung != 0 {
		t.Fatalf("speaker's own rumor should stay first-hand rung-0, got %+v", hannah)
	}
}
