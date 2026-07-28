package sim_test

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// pay_with_item_trade_churn_test.go — LLM-555. The gate half: a sale made in an
// EARLIER conversation still bars buying that good straight back off the same
// person.
//
// This is the case the LLM-189 gate was shaped for and could never see. Its arm 2
// scopes to the caller's CURRENT huddle and walks ledger entries that are reaped
// an hour after they resolve, while every reversal observed in the live ledger
// crossed a huddle boundary — 55 of 55 since 2026-05-06. Live 2026-07-28: iron
// went factor -> Josiah at 19:53, back Josiah -> factor at 20:50, and Josiah was
// buying it off him again at 21:32.
//
// Helpers (buildPayWithItemWorld, seedQuote, seedLedgerEntry, mustSend) live in
// pay_with_item_commands_test.go — same sim_test package.

// rememberSoldTo stamps the seller's sold-to memory the way an accepted sale in an
// earlier conversation would have left it. Seeded rather than driven through a
// real sale so the fixture states plainly that the sale is NOT in this huddle and
// NOT in the ledger — which is the whole point of the case. The capture half (that
// an accepted sale writes exactly this) is covered in trade_reversal_internal_test.go.
func rememberSoldTo(t *testing.T, w *sim.World, seller sim.ActorID, buyer sim.ActorID, kind sim.ItemKind, at time.Time) {
	t.Helper()
	mustSend(t, w, func(world *sim.World) {
		world.Actors[seller].Observed.Observe(
			sim.ObservedStateKey{PeerID: buyer, ItemKind: kind, Condition: sim.ObservedSoldToPeer},
			at,
		)
	})
}

// buildChurnWorld seeds the live shape: Hannah sells ale, Josiah keeps the
// store and holds ale of his own, and the two are in one conversation.
func buildChurnWorld(t *testing.T) (*sim.World, func(), time.Time) {
	t.Helper()
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "hannah", displayName: "Hannah", kind: sim.KindNPCShared, huddleID: "h1", coins: 30, inventory: map[sim.ItemKind]int{"ale": 2, "bread": 4}},
		{id: "josiah", displayName: "Josiah", kind: sim.KindNPCStateful, huddleID: "h1", coins: 30, inventory: map[sim.ItemKind]int{"ale": 20, "bread": 5}},
	})
	return w, stop, time.Now().UTC()
}

func TestPayWithItem_TradeChurnGate(t *testing.T) {
	const wantSteer = "you don't buy your own goods straight back"

	// The arbitrating case. The sale is in NEITHER the current huddle nor the
	// ledger — exactly the state the engine is in an hour later, once the
	// conversation has ended and the terminal entry has been reaped. Before this
	// fix the offer minted and the round trip completed.
	t.Run("sale_in_an_earlier_conversation_blocks_the_buy_back", func(t *testing.T) {
		w, stop, at := buildChurnWorld(t)
		defer stop()
		rememberSoldTo(t, w, "hannah", "josiah", "ale", at.Add(-time.Hour))

		_, err := w.Send(sim.PayWithItem("hannah", "Josiah", "ale", 1, 4, false, nil, nil, 0, 0, "", at))
		if err == nil || !strings.Contains(err.Error(), wantSteer) {
			t.Fatalf("buying back what she sold him err = %v, want the churn steer", err)
		}
	})

	// The steer must not borrow the neighbouring gate's wording. "Wait for them to
	// pay you" is sound mid-negotiation and misleading about a sale made an hour
	// ago in another room: there is no payment coming to wait for, and an NPC told
	// to wait for one will stand there.
	t.Run("steer_does_not_tell_her_to_wait_for_a_payment", func(t *testing.T) {
		w, stop, at := buildChurnWorld(t)
		defer stop()
		rememberSoldTo(t, w, "hannah", "josiah", "ale", at.Add(-time.Hour))

		_, err := w.Send(sim.PayWithItem("hannah", "Josiah", "ale", 1, 4, false, nil, nil, 0, 0, "", at))
		if err == nil {
			t.Fatal("expected the churn gate to reject")
		}
		if strings.Contains(err.Error(), "Wait for them to pay you") {
			t.Errorf("churn steer must not promise a payment that is not coming: %q", err.Error())
		}
		if !strings.Contains(err.Error(), "post the offer first") {
			t.Errorf("churn steer must name the way out — the counterparty posting an offer: %q", err.Error())
		}
	})

	// LLM-551's escape hatch, which this gate needs MORE than the one it sits
	// beside: that gate's reach ends with the huddle, this memory lasts three
	// hours. A counterparty who posts a targeted quote is saying "I do mean to
	// sell you this", which is precisely what the steer above tells her to ask
	// for — refusing it would make the way out a dead end.
	t.Run("a_targeted_quote_from_him_reopens_the_trade", func(t *testing.T) {
		w, stop, at := buildChurnWorld(t)
		defer stop()
		rememberSoldTo(t, w, "hannah", "josiah", "ale", at.Add(-time.Hour))
		seedQuote(t, w, sim.SceneQuote{
			ID: 20, SceneID: "sc1", SellerID: "josiah", TargetBuyer: "hannah",
			Lines: []sim.QuoteLine{{ItemKind: "ale", Qty: 1}}, Amount: 4,
			State: sim.SceneQuoteStateActive, CreatedAt: at, ExpiresAt: at.Add(10 * time.Minute),
		})

		if _, err := w.Send(sim.PayWithItem("hannah", "Josiah", "ale", 1, 4, false, nil, nil, 0, 0, "", at)); err != nil {
			t.Fatalf("a quote he posted her must be takeable: %v", err)
		}
	})

	// The two cases that keep the distributor whole. Buying in and selling on is
	// his entire function; only the round trip with one partner over one good is
	// the defect.
	t.Run("a_different_good_from_the_same_man_is_untouched", func(t *testing.T) {
		w, stop, at := buildChurnWorld(t)
		defer stop()
		rememberSoldTo(t, w, "hannah", "josiah", "ale", at.Add(-time.Hour))

		res, err := w.Send(sim.PayWithItem("hannah", "Josiah", "bread", 1, 4, false, nil, nil, 0, 0, "", at))
		if err != nil {
			t.Fatalf("an unrelated good must still be buyable: %v", err)
		}
		if res.(sim.PayWithItemResult).State != sim.PayLedgerStatePending {
			t.Errorf("state = %q, want pending", res.(sim.PayWithItemResult).State)
		}
	})

	t.Run("the_memory_lapses_and_the_pair_may_deal_again", func(t *testing.T) {
		w, stop, at := buildChurnWorld(t)
		defer stop()
		rememberSoldTo(t, w, "hannah", "josiah", "ale", at.Add(-sim.SoldToPeerMemoryTTL-time.Minute))

		if _, err := w.Send(sim.PayWithItem("hannah", "Josiah", "ale", 1, 4, false, nil, nil, 0, 0, "", at)); err != nil {
			t.Fatalf("past the TTL the pair may trade in that direction again: %v", err)
		}
	})

	// Directional: the memory lives on the SELLER, so the man who BOUGHT is free
	// to buy more of the same. A memory stamped on both sides would suppress an
	// ordinary repeat purchase.
	t.Run("the_buyer_may_buy_more_of_the_same", func(t *testing.T) {
		w, stop, at := buildChurnWorld(t)
		defer stop()
		rememberSoldTo(t, w, "hannah", "josiah", "ale", at.Add(-time.Hour))

		if _, err := w.Send(sim.PayWithItem("josiah", "Hannah", "ale", 1, 4, false, nil, nil, 0, 0, "", at)); err != nil {
			t.Fatalf("the buyer of the first sale must be able to buy more: %v", err)
		}
	})
}
