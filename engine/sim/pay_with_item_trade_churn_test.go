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

// TestPayWithItem_TradeChurnEscapeHatchIsNarrow pins the ways OUT of the block
// that must not work. The escape is the only override on a three-hour memory, so
// a quote that is expired, posted in another scene, or aimed at someone else must
// not open the door — otherwise the gate is defeated by a stale artifact rather
// than by the counterparty actually offering (code_review).
func TestPayWithItem_TradeChurnEscapeHatchIsNarrow(t *testing.T) {
	const wantSteer = "you don't buy your own goods straight back"

	cases := []struct {
		name  string
		quote func(at time.Time) sim.SceneQuote
	}{
		{
			name: "expired_quote",
			quote: func(at time.Time) sim.SceneQuote {
				return sim.SceneQuote{
					ID: 30, SceneID: "sc1", SellerID: "josiah", TargetBuyer: "hannah",
					Lines: []sim.QuoteLine{{ItemKind: "ale", Qty: 1}}, Amount: 4,
					State: sim.SceneQuoteStateActive, CreatedAt: at.Add(-time.Hour), ExpiresAt: at.Add(-time.Minute),
				}
			},
		},
		{
			name: "quote_in_another_scene",
			quote: func(at time.Time) sim.SceneQuote {
				return sim.SceneQuote{
					ID: 31, SceneID: "sc-elsewhere", SellerID: "josiah", TargetBuyer: "hannah",
					Lines: []sim.QuoteLine{{ItemKind: "ale", Qty: 1}}, Amount: 4,
					State: sim.SceneQuoteStateActive, CreatedAt: at, ExpiresAt: at.Add(10 * time.Minute),
				}
			},
		},
		{
			name: "quote_aimed_at_someone_else",
			quote: func(at time.Time) sim.SceneQuote {
				return sim.SceneQuote{
					ID: 32, SceneID: "sc1", SellerID: "josiah", TargetBuyer: "someone-else",
					Lines: []sim.QuoteLine{{ItemKind: "ale", Qty: 1}}, Amount: 4,
					State: sim.SceneQuoteStateActive, CreatedAt: at, ExpiresAt: at.Add(10 * time.Minute),
				}
			},
		},
		{
			name: "quote_no_longer_active",
			quote: func(at time.Time) sim.SceneQuote {
				return sim.SceneQuote{
					ID: 33, SceneID: "sc1", SellerID: "josiah", TargetBuyer: "hannah",
					Lines: []sim.QuoteLine{{ItemKind: "ale", Qty: 1}}, Amount: 4,
					State: sim.SceneQuoteStateTaken, CreatedAt: at, ExpiresAt: at.Add(10 * time.Minute),
				}
			},
		},
		{
			name: "quote_for_a_different_good",
			quote: func(at time.Time) sim.SceneQuote {
				return sim.SceneQuote{
					ID: 34, SceneID: "sc1", SellerID: "josiah", TargetBuyer: "hannah",
					Lines: []sim.QuoteLine{{ItemKind: "bread", Qty: 1}}, Amount: 4,
					State: sim.SceneQuoteStateActive, CreatedAt: at, ExpiresAt: at.Add(10 * time.Minute),
				}
			},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			w, stop, at := buildChurnWorld(t)
			defer stop()
			rememberSoldTo(t, w, "hannah", "josiah", "ale", at.Add(-time.Hour))
			seedQuote(t, w, tc.quote(at))

			_, err := w.Send(sim.PayWithItem("hannah", "Josiah", "ale", 1, 4, false, nil, nil, 0, 0, "", at))
			if err == nil || !strings.Contains(err.Error(), wantSteer) {
				t.Fatalf("%s must not open the buy-back: err = %v", tc.name, err)
			}
		})
	}
}

// TestPayWithItem_TradeChurnMemoryWrittenByTheRealSubscriber closes the loop the
// unit tests leave open: registration is part of the production change, so the
// memory must be shown landing from a REAL accepted sale through the real
// subscriber, not only from a hand-built event (code_review). It then proves the
// consequence end to end — the reverse buy this sale enables is refused.
func TestPayWithItem_TradeChurnMemoryWrittenByTheRealSubscriber(t *testing.T) {
	w, stop, at := buildChurnWorld(t)
	defer stop()
	mustSend(t, w, func(world *sim.World) {
		sim.RegisterTradeReversalSubscriber(world)
	})

	// Josiah buys 2 ale from Hannah and she accepts: a real sale, Hannah selling.
	res, err := w.Send(sim.PayWithItem("josiah", "Hannah", "ale", 2, 6, false, nil, nil, 0, 0, "", at))
	if err != nil {
		t.Fatalf("offer: %v", err)
	}
	ledgerID := res.(sim.PayWithItemResult).LedgerID
	if _, err := w.Send(sim.AcceptPay("hannah", ledgerID, at)); err != nil {
		t.Fatalf("accept: %v", err)
	}

	// Advance to the state the engine is actually in an hour later: the terminal
	// entry has been reaped (PayLedgerInResponseToWindow, 1h), so the ledger
	// evidence arm 2 reads is gone. Without this the LLM-189 gate answers first —
	// correctly, since within one conversation the ledger IS the direct evidence
	// and its steer is the right one. What must survive the reap is the memory.
	mustSend(t, w, func(world *sim.World) {
		delete(world.PayLedger, ledgerID)
	})
	later := at.Add(90 * time.Minute)

	// Hannah now tries to buy that same ale back off him — the round trip.
	_, err = w.Send(sim.PayWithItem("hannah", "Josiah", "ale", 1, 4, false, nil, nil, 0, 0, "", later))
	if err == nil || !strings.Contains(err.Error(), "you don't buy your own goods straight back") {
		t.Fatalf("a real sale must leave a memory that outlives the ledger and bars the buy-back: err = %v", err)
	}
}

// TestPayWithItem_TradeChurnBundleTakeStampsEveryLine drives a REAL bundle
// quote-take rather than a hand-built event, which is the only way to confirm
// that PayWithItemResolved.Lines is authoritative for what actually transferred
// (code_review). A bundle leaves ItemKind empty, so if Lines were merely the
// caller's request — or were dropped on the settled path — a bundle sale would be
// a hole the churn walks straight through.
func TestPayWithItem_TradeChurnBundleTakeStampsEveryLine(t *testing.T) {
	w, stop, at := buildChurnWorld(t)
	defer stop()
	mustSend(t, w, func(world *sim.World) {
		sim.RegisterTradeReversalSubscriber(world)
	})
	// Hannah posts Josiah a two-line bundle and he takes it: she is the seller of
	// both goods.
	seedQuote(t, w, sim.SceneQuote{
		ID: 40, SceneID: "sc1", SellerID: "hannah", TargetBuyer: "josiah",
		Lines: []sim.QuoteLine{{ItemKind: "ale", Qty: 1}, {ItemKind: "bread", Qty: 1}}, Amount: 6,
		State: sim.SceneQuoteStateActive, CreatedAt: at, ExpiresAt: at.Add(10 * time.Minute),
	})
	// The buyer echoes one representative line plus the bundle total; the engine
	// grants the WHOLE bundle off quote_id 40.
	res, err := w.Send(sim.PayWithItem("josiah", "Hannah", "ale", 1, 6, false, nil, nil, 40, 0, "", at))
	if err != nil {
		t.Fatalf("bundle take: %v", err)
	}
	if !res.(sim.PayWithItemResult).FastPath {
		t.Fatal("expected the bundle take to settle on the fast path")
	}

	// Both goods must now bar the reverse leg, not just whichever one a
	// single-item field would have carried.
	mustSend(t, w, func(world *sim.World) {
		for id := range world.PayLedger {
			delete(world.PayLedger, id) // the 1h reap, so only the memory can answer
		}
	})
	later := at.Add(90 * time.Minute)
	for _, kind := range []string{"ale", "bread"} {
		_, err := w.Send(sim.PayWithItem("hannah", "Josiah", kind, 1, 4, false, nil, nil, 0, 0, "", later))
		if err == nil || !strings.Contains(err.Error(), "you don't buy your own goods straight back") {
			t.Errorf("every line of a settled bundle must bar its own buy-back; %s err = %v", kind, err)
		}
	}
}
