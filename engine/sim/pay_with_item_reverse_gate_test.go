package sim_test

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// pay_with_item_reverse_gate_test.go — LLM-189. Coverage for the two
// halves of the reverse-pay fix:
//
//   - close-quote-on-take: a settled quote flips to SceneQuoteStateTaken so
//     it stops rendering as a phantom standing offer and can't be re-taken.
//   - the reverse-pay role-gate: a seller can't fire pay_with_item at the
//     counterparty she is selling THIS item to (active quote OR just-closed
//     sale in this huddle).
//
// Helpers (buildPayWithItemWorld, seedQuote, seedLedgerEntry, mustSend) live
// in pay_with_item_commands_test.go — same sim_test package.

// readQuoteState reads a quote's live state on the world goroutine.
func readQuoteState(t *testing.T, w *sim.World, id sim.QuoteID) sim.SceneQuoteState {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		q, ok := world.Quotes[id]
		if !ok || q == nil {
			return sim.SceneQuoteState("<missing>"), nil
		}
		return q.State, nil
	}})
	if err != nil {
		t.Fatalf("read quote %d: %v", id, err)
	}
	return res.(sim.SceneQuoteState)
}

// sceneHasQuoteIndexed reports whether the scene's reverse index still lists
// the quote — closing a quote must drop it from the index too.
func sceneHasQuoteIndexed(t *testing.T, w *sim.World, sceneID sim.SceneID, id sim.QuoteID) bool {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		scene, ok := world.Scenes[sceneID]
		if !ok || scene == nil {
			return false, nil
		}
		for _, q := range scene.QuoteIDs {
			if q == id {
				return true, nil
			}
		}
		return false, nil
	}})
	if err != nil {
		t.Fatalf("read scene index: %v", err)
	}
	return res.(bool)
}

// buildReverseGateWorld seeds a vendor (Prudence) selling bread and a
// co-present customer (Anne). Bread is portable, so a consume_now=false take
// stays take-home — no eat-here clamp to perturb the auto-match terms.
func buildReverseGateWorld(t *testing.T) (*sim.World, func(), time.Time) {
	t.Helper()
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "prudence", displayName: "Prudence", kind: sim.KindNPCStateful, huddleID: "h1", coins: 30, inventory: map[sim.ItemKind]int{"bread": 5}},
		{id: "anne", displayName: "Anne", kind: sim.KindNPCShared, huddleID: "h1", coins: 30, inventory: map[sim.ItemKind]int{"bread": 4}},
	})
	return w, stop, time.Now().UTC()
}

// TestPayWithItem_QuoteClosedOnTake — a settled quote flips to Taken (both the
// explicit quote_id fast path and the bare-offer auto-match path), drops from
// the scene index, and can't be taken a second time.
func TestPayWithItem_QuoteClosedOnTake(t *testing.T) {
	t.Run("explicit_quote_id", func(t *testing.T) {
		w, stop, at := buildReverseGateWorld(t)
		defer stop()
		seedQuote(t, w, sim.SceneQuote{
			ID: 8, SceneID: "sc1", SellerID: "prudence", TargetBuyer: "anne",
			Lines: []sim.QuoteLine{{ItemKind: "bread", Qty: 1}}, Amount: 4,
			State: sim.SceneQuoteStateActive, CreatedAt: at, ExpiresAt: at.Add(10 * time.Minute),
		})

		res, err := w.Send(sim.PayWithItem("anne", "Prudence", "bread", 1, 4, false, nil, nil, 8, 0, "", at))
		if err != nil {
			t.Fatalf("take: %v", err)
		}
		if !res.(sim.PayWithItemResult).FastPath {
			t.Fatal("expected fast-path take")
		}
		if got := readQuoteState(t, w, 8); got != sim.SceneQuoteStateTaken {
			t.Errorf("quote state = %q, want taken", got)
		}
		if sceneHasQuoteIndexed(t, w, "sc1", 8) {
			t.Error("taken quote still in scene index")
		}
		// A second take of a now-closed quote rejects rather than re-settling.
		_, err = w.Send(sim.PayWithItem("anne", "Prudence", "bread", 1, 4, false, nil, nil, 8, 0, "", at))
		if err == nil || !strings.Contains(err.Error(), "no longer active") {
			t.Fatalf("second take err = %v, want 'no longer active'", err)
		}
	})

	t.Run("auto_match", func(t *testing.T) {
		w, stop, at := buildReverseGateWorld(t)
		defer stop()
		seedQuote(t, w, sim.SceneQuote{
			ID: 9, SceneID: "sc1", SellerID: "prudence", TargetBuyer: "anne",
			Lines: []sim.QuoteLine{{ItemKind: "bread", Qty: 1}}, Amount: 4,
			State: sim.SceneQuoteStateActive, CreatedAt: at, ExpiresAt: at.Add(10 * time.Minute),
		})

		// Bare offer (quote_id 0) — auto-matches the standing quote.
		res, err := w.Send(sim.PayWithItem("anne", "Prudence", "bread", 1, 4, false, nil, nil, 0, 0, "", at))
		if err != nil {
			t.Fatalf("auto-match take: %v", err)
		}
		if !res.(sim.PayWithItemResult).FastPath {
			t.Fatal("expected auto-match to settle on the fast path")
		}
		if got := readQuoteState(t, w, 9); got != sim.SceneQuoteStateTaken {
			t.Errorf("auto-matched quote state = %q, want taken", got)
		}
		if sceneHasQuoteIndexed(t, w, "sc1", 9) {
			t.Error("auto-matched-then-taken quote still in scene index")
		}
		// A second bare offer no longer auto-matches the closed quote — it
		// falls through to a slow-path mint instead of re-settling.
		res2, err := w.Send(sim.PayWithItem("anne", "Prudence", "bread", 1, 4, false, nil, nil, 0, 0, "", at))
		if err != nil {
			t.Fatalf("second bare offer: %v", err)
		}
		if res2.(sim.PayWithItemResult).FastPath {
			t.Error("second bare offer fast-pathed — taken quote was still auto-matchable")
		}
	})
}

// TestPayWithItem_ReverseSaleGate — a seller can't fire pay_with_item at the
// counterparty she is selling the same item to. Both signals reject; a
// legitimate vendor-as-buyer (different item, or no standing sale to that
// peer) is untouched.
func TestPayWithItem_ReverseSaleGate(t *testing.T) {
	const wantSteer = "you don't buy it back"

	t.Run("active_sell_quote_blocks_reverse", func(t *testing.T) {
		w, stop, at := buildReverseGateWorld(t)
		defer stop()
		// Prudence is selling bread to Anne (targeted quote).
		seedQuote(t, w, sim.SceneQuote{
			ID: 10, SceneID: "sc1", SellerID: "prudence", TargetBuyer: "anne",
			Lines: []sim.QuoteLine{{ItemKind: "bread", Qty: 1}}, Amount: 4,
			State: sim.SceneQuoteStateActive, CreatedAt: at, ExpiresAt: at.Add(10 * time.Minute),
		})
		// Prudence (the seller) tries to BUY bread from Anne — the inversion.
		_, err := w.Send(sim.PayWithItem("prudence", "Anne", "bread", 1, 4, false, nil, nil, 0, 0, "", at))
		if err == nil || !strings.Contains(err.Error(), wantSteer) {
			t.Fatalf("reverse offer err = %v, want reverse-pay steer", err)
		}
	})

	t.Run("public_sell_quote_does_not_block", func(t *testing.T) {
		w, stop, at := buildReverseGateWorld(t)
		defer stop()
		// A PUBLIC bread quote (no TargetBuyer) ties no specific buyer to the
		// sale, so it must NOT block a legitimate restock — Prudence advertising
		// bread to the room while buying bread from a co-present supplier (Anne).
		// The gate keys on a TARGETED quote (arm 1) or a concrete accepted sale
		// (arm 2); a public quote alone is neither.
		seedQuote(t, w, sim.SceneQuote{
			ID: 11, SceneID: "sc1", SellerID: "prudence",
			Lines: []sim.QuoteLine{{ItemKind: "bread", Qty: 1}}, Amount: 4,
			State: sim.SceneQuoteStateActive, CreatedAt: at, ExpiresAt: at.Add(10 * time.Minute),
		})
		res, err := w.Send(sim.PayWithItem("prudence", "Anne", "bread", 1, 4, false, nil, nil, 0, 0, "", at))
		if err != nil {
			t.Fatalf("public-quote-only seller buying the item should not be gated: %v", err)
		}
		if res.(sim.PayWithItemResult).State != sim.PayLedgerStatePending {
			t.Errorf("state = %q, want pending (mints normally)", res.(sim.PayWithItemResult).State)
		}
	})

	t.Run("accepted_sale_blocks_reverse", func(t *testing.T) {
		w, stop, at := buildReverseGateWorld(t)
		defer stop()
		// Prudence already sold Anne bread in this huddle (accepted ledger
		// entry) — the just-closed deal the live model misread as still open.
		seedLedgerEntry(t, w, sim.PayLedgerEntry{
			ID: 100, BuyerID: "anne", SellerID: "prudence", ItemKind: "bread", Qty: 5,
			Amount: 10, State: sim.PayLedgerStateAccepted, CreatedAt: at, ResolvedAt: at,
			SceneID: "sc1", HuddleID: "h1",
		})
		_, err := w.Send(sim.PayWithItem("prudence", "Anne", "bread", 1, 4, false, nil, nil, 0, 0, "", at))
		if err == nil || !strings.Contains(err.Error(), wantSteer) {
			t.Fatalf("reverse offer after sale err = %v, want reverse-pay steer", err)
		}
	})

	t.Run("legit_buy_of_other_item_allowed", func(t *testing.T) {
		w, stop, at := buildReverseGateWorld(t)
		defer stop()
		mustSend(t, w, func(world *sim.World) {
			world.Actors["anne"].Inventory["ale"] = 3
		})
		// Prudence sells BREAD to Anne...
		seedQuote(t, w, sim.SceneQuote{
			ID: 12, SceneID: "sc1", SellerID: "prudence", TargetBuyer: "anne",
			Lines: []sim.QuoteLine{{ItemKind: "bread", Qty: 1}}, Amount: 4,
			State: sim.SceneQuoteStateActive, CreatedAt: at, ExpiresAt: at.Add(10 * time.Minute),
		})
		// ...but buying ALE from Anne is a different good — the gate is
		// item-scoped, so this is a legitimate offer that mints normally.
		res, err := w.Send(sim.PayWithItem("prudence", "Anne", "ale", 1, 4, false, nil, nil, 0, 0, "", at))
		if err != nil {
			t.Fatalf("buying a different item should not be gated: %v", err)
		}
		if res.(sim.PayWithItemResult).State != sim.PayLedgerStatePending {
			t.Errorf("ale offer state = %q, want pending (slow-path mint)", res.(sim.PayWithItemResult).State)
		}
	})

	// LLM-551 — the live Patience Walker deadlock. She sold Josiah Thorne flour
	// early in a huddle, he later posted her a targeted flour quote, and every
	// one of her ~20 exactly-matching pay_with_item calls was refused by arm 2
	// because the huddle was still the same one. Roles here: Anne is the buyer
	// who sold first, Prudence the keeper who then offers.
	t.Run("counter_directional_quote_unblocks_after_own_sale", func(t *testing.T) {
		w, stop, at := buildReverseGateWorld(t)
		defer stop()
		// Anne sold Prudence bread earlier in THIS huddle — arm 2 evidence.
		seedLedgerEntry(t, w, sim.PayLedgerEntry{
			ID: 200, BuyerID: "prudence", SellerID: "anne", ItemKind: "bread", Qty: 3,
			Amount: 7, State: sim.PayLedgerStateAccepted, CreatedAt: at, ResolvedAt: at,
			SceneID: "sc1", HuddleID: "h1",
		})
		// Prudence now offers Anne bread — direction settled by the substrate.
		seedQuote(t, w, sim.SceneQuote{
			ID: 20, SceneID: "sc1", SellerID: "prudence", TargetBuyer: "anne",
			Lines: []sim.QuoteLine{{ItemKind: "bread", Qty: 1}}, Amount: 4,
			State: sim.SceneQuoteStateActive, CreatedAt: at, ExpiresAt: at.Add(10 * time.Minute),
		})
		// A bare offer, exactly as the live model sent it — no quote_id, because
		// the warrant carrying it was consumed turns ago.
		res, err := w.Send(sim.PayWithItem("anne", "Prudence", "bread", 1, 4, false, nil, nil, 0, 0, "", at))
		if err != nil {
			t.Fatalf("buyer taking a standing quote after her own earlier sale was gated: %v", err)
		}
		if !res.(sim.PayWithItemResult).FastPath {
			t.Error("expected the standing quote to be auto-matched and settled")
		}
		if got := readQuoteState(t, w, 20); got != sim.SceneQuoteStateTaken {
			t.Errorf("quote state = %q, want taken", got)
		}
	})

	// The escape is DELIBERATELY item-scoped rather than term-matched. Once the
	// counterparty has posted this buyer a quote for this good, direction is
	// settled between them and the role-gate has nothing left to protect; the
	// terms are a negotiation, and a mismatched ask simply mints an ordinary
	// pending offer the seller may accept, decline or counter. Term-matching the
	// escape would re-open the bug at one remove: Josiah quotes four sacks,
	// Patience decides she wants two, and she is gated out of her own purchase
	// again. Pinned so a later reader doesn't "tighten" it back into the defect.
	t.Run("counter_quote_escape_is_item_scoped_not_term_matched", func(t *testing.T) {
		for _, c := range []struct {
			name         string
			qty, amount  int
			wantFastPath bool
		}{
			{"exact terms settle at once", 1, 4, true},
			{"fewer than quoted mints an offer", 1, 3, false},
			{"more than quoted mints an offer", 3, 12, false},
		} {
			t.Run(c.name, func(t *testing.T) {
				w, stop, at := buildReverseGateWorld(t)
				defer stop()
				seedLedgerEntry(t, w, sim.PayLedgerEntry{
					ID: 210, BuyerID: "prudence", SellerID: "anne", ItemKind: "bread", Qty: 3,
					Amount: 7, State: sim.PayLedgerStateAccepted, CreatedAt: at, ResolvedAt: at,
					SceneID: "sc1", HuddleID: "h1",
				})
				seedQuote(t, w, sim.SceneQuote{
					ID: 30, SceneID: "sc1", SellerID: "prudence", TargetBuyer: "anne",
					Lines: []sim.QuoteLine{{ItemKind: "bread", Qty: 1}}, Amount: 4,
					State: sim.SceneQuoteStateActive, CreatedAt: at, ExpiresAt: at.Add(10 * time.Minute),
				})
				res, err := w.Send(sim.PayWithItem("anne", "Prudence", "bread", c.qty, c.amount, false, nil, nil, 0, 0, "", at))
				if err != nil {
					t.Fatalf("err = %v, want the buy to proceed (the gate has nothing to protect here)", err)
				}
				got := res.(sim.PayWithItemResult)
				if got.FastPath != c.wantFastPath {
					t.Errorf("FastPath = %v, want %v", got.FastPath, c.wantFastPath)
				}
				if !c.wantFastPath && got.State != sim.PayLedgerStatePending {
					t.Errorf("state = %q, want pending — a mismatched ask is a negotiation, not a refusal", got.State)
				}
			})
		}
	})

	t.Run("own_sale_still_blocks_without_counter_quote", func(t *testing.T) {
		w, stop, at := buildReverseGateWorld(t)
		defer stop()
		// Same accepted sale, but the counterparty has posted NOTHING. The
		// LLM-189 protection must survive the LLM-551 escape.
		seedLedgerEntry(t, w, sim.PayLedgerEntry{
			ID: 201, BuyerID: "prudence", SellerID: "anne", ItemKind: "bread", Qty: 3,
			Amount: 7, State: sim.PayLedgerStateAccepted, CreatedAt: at, ResolvedAt: at,
			SceneID: "sc1", HuddleID: "h1",
		})
		_, err := w.Send(sim.PayWithItem("anne", "Prudence", "bread", 1, 4, false, nil, nil, 0, 0, "", at))
		if err == nil || !strings.Contains(err.Error(), wantSteer) {
			t.Fatalf("err = %v, want the reverse-pay steer to still fire", err)
		}
	})

	// A quote the seller can no longer cover is NOT evidence of settled
	// direction. This is load-bearing on reconcileQuoteCoverage (LLM-409), which
	// runs once per command on the world goroutine before republish and flips any
	// uncoverable lot to terminal shortfall — so an Active quote in the live map
	// is coverable as of the last command boundary, with no sweep latency. The
	// escape reads State, and that is sufficient BECAUSE of the reconcile; this
	// test pins the coupling so a change to either end surfaces here.
	t.Run("uncoverable_counter_quote_does_not_unblock", func(t *testing.T) {
		w, stop, at := buildReverseGateWorld(t)
		defer stop()
		seedLedgerEntry(t, w, sim.PayLedgerEntry{
			ID: 220, BuyerID: "prudence", SellerID: "anne", ItemKind: "bread", Qty: 3,
			Amount: 7, State: sim.PayLedgerStateAccepted, CreatedAt: at, ResolvedAt: at,
			SceneID: "sc1", HuddleID: "h1",
		})
		seedQuote(t, w, sim.SceneQuote{
			ID: 31, SceneID: "sc1", SellerID: "prudence", TargetBuyer: "anne",
			Lines: []sim.QuoteLine{{ItemKind: "bread", Qty: 1}}, Amount: 4,
			State: sim.SceneQuoteStateShortfall, CreatedAt: at, ExpiresAt: at.Add(10 * time.Minute),
		})
		_, err := w.Send(sim.PayWithItem("anne", "Prudence", "bread", 1, 4, false, nil, nil, 0, 0, "", at))
		if err == nil || !strings.Contains(err.Error(), wantSteer) {
			t.Fatalf("err = %v, want a lot the seller can't cover NOT to lift the gate", err)
		}
	})

	// An expired quote is likewise no evidence — the escape and the fast path
	// must agree on the deadline or the gate opens onto a call that then rejects.
	t.Run("expired_counter_quote_does_not_unblock", func(t *testing.T) {
		w, stop, at := buildReverseGateWorld(t)
		defer stop()
		seedLedgerEntry(t, w, sim.PayLedgerEntry{
			ID: 221, BuyerID: "prudence", SellerID: "anne", ItemKind: "bread", Qty: 3,
			Amount: 7, State: sim.PayLedgerStateAccepted, CreatedAt: at, ResolvedAt: at,
			SceneID: "sc1", HuddleID: "h1",
		})
		seedQuote(t, w, sim.SceneQuote{
			ID: 32, SceneID: "sc1", SellerID: "prudence", TargetBuyer: "anne",
			Lines: []sim.QuoteLine{{ItemKind: "bread", Qty: 1}}, Amount: 4,
			State: sim.SceneQuoteStateActive, CreatedAt: at.Add(-time.Hour),
			ExpiresAt: at.Add(-time.Second), // lapsed; the sweep hasn't run yet
		})
		_, err := w.Send(sim.PayWithItem("anne", "Prudence", "bread", 1, 4, false, nil, nil, 0, 0, "", at))
		if err == nil || !strings.Contains(err.Error(), wantSteer) {
			t.Fatalf("err = %v, want an expired quote NOT to lift the gate", err)
		}
	})

	t.Run("public_counter_quote_does_not_unblock", func(t *testing.T) {
		w, stop, at := buildReverseGateWorld(t)
		defer stop()
		seedLedgerEntry(t, w, sim.PayLedgerEntry{
			ID: 202, BuyerID: "prudence", SellerID: "anne", ItemKind: "bread", Qty: 3,
			Amount: 7, State: sim.PayLedgerStateAccepted, CreatedAt: at, ResolvedAt: at,
			SceneID: "sc1", HuddleID: "h1",
		})
		// An untargeted advertisement to the room is not direction settled
		// between these two — the escape requires a quote in the caller's name.
		seedQuote(t, w, sim.SceneQuote{
			ID: 21, SceneID: "sc1", SellerID: "prudence",
			Lines: []sim.QuoteLine{{ItemKind: "bread", Qty: 1}}, Amount: 4,
			State: sim.SceneQuoteStateActive, CreatedAt: at, ExpiresAt: at.Add(10 * time.Minute),
		})
		_, err := w.Send(sim.PayWithItem("anne", "Prudence", "bread", 1, 4, false, nil, nil, 0, 0, "", at))
		if err == nil || !strings.Contains(err.Error(), wantSteer) {
			t.Fatalf("err = %v, want a public quote NOT to lift the gate", err)
		}
	})

	// LLM-551: the rejection's second sentence must name a verb the reader can
	// actually use. accept_pay answers a buyer's pay offer, so it belongs in the
	// text only when such an offer is genuinely pending — the live steer named it
	// unconditionally and sent a would-be buyer after a tool she never called.
	t.Run("steer_names_accept_pay_only_when_an_offer_is_pending", func(t *testing.T) {
		t.Run("no_pending_offer", func(t *testing.T) {
			w, stop, at := buildReverseGateWorld(t)
			defer stop()
			seedLedgerEntry(t, w, sim.PayLedgerEntry{
				ID: 203, BuyerID: "anne", SellerID: "prudence", ItemKind: "bread", Qty: 5,
				Amount: 10, State: sim.PayLedgerStateAccepted, CreatedAt: at, ResolvedAt: at,
				SceneID: "sc1", HuddleID: "h1",
			})
			_, err := w.Send(sim.PayWithItem("prudence", "Anne", "bread", 1, 4, false, nil, nil, 0, 0, "", at))
			if err == nil {
				t.Fatal("expected the reverse-pay rejection")
			}
			if strings.Contains(err.Error(), "accept_pay") {
				t.Errorf("steer names accept_pay with no offer to accept: %v", err)
			}
			if !strings.Contains(err.Error(), "they must post the offer first") {
				t.Errorf("steer does not point at the buy path: %v", err)
			}
		})

		t.Run("offer_pending", func(t *testing.T) {
			w, stop, at := buildReverseGateWorld(t)
			defer stop()
			seedLedgerEntry(t, w, sim.PayLedgerEntry{
				ID: 204, BuyerID: "anne", SellerID: "prudence", ItemKind: "bread", Qty: 5,
				Amount: 10, State: sim.PayLedgerStateAccepted, CreatedAt: at, ResolvedAt: at,
				SceneID: "sc1", HuddleID: "h1",
			})
			// Anne has a live pay offer awaiting Prudence's decision — here
			// accept_pay IS the move, and the id it needs must ride along.
			seedLedgerEntry(t, w, sim.PayLedgerEntry{
				ID: 205, BuyerID: "anne", SellerID: "prudence", ItemKind: "bread", Qty: 1,
				Amount: 4, State: sim.PayLedgerStatePending, CreatedAt: at,
				SceneID: "sc1", HuddleID: "h1", ExpiresAt: at.Add(3 * time.Minute),
			})
			_, err := w.Send(sim.PayWithItem("prudence", "Anne", "bread", 1, 4, false, nil, nil, 0, 0, "", at))
			if err == nil {
				t.Fatal("expected the reverse-pay rejection")
			}
			if !strings.Contains(err.Error(), "accept_pay, offer id 205") {
				t.Errorf("steer should name accept_pay + the pending offer id: %v", err)
			}
		})
	})

	t.Run("normal_buy_direction_unaffected", func(t *testing.T) {
		w, stop, at := buildReverseGateWorld(t)
		defer stop()
		// Prudence sells bread to Anne; Anne buying it (the correct direction)
		// is never gated — Anne is not the seller of bread to Prudence.
		seedQuote(t, w, sim.SceneQuote{
			ID: 13, SceneID: "sc1", SellerID: "prudence", TargetBuyer: "anne",
			Lines: []sim.QuoteLine{{ItemKind: "bread", Qty: 1}}, Amount: 4,
			State: sim.SceneQuoteStateActive, CreatedAt: at, ExpiresAt: at.Add(10 * time.Minute),
		})
		res, err := w.Send(sim.PayWithItem("anne", "Prudence", "bread", 1, 4, false, nil, nil, 13, 0, "", at))
		if err != nil {
			t.Fatalf("normal buy should settle: %v", err)
		}
		if !res.(sim.PayWithItemResult).FastPath {
			t.Error("normal buy did not take the quote")
		}
	})
}
